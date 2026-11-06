package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"codr.my/codr/internal/config"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/wiki"
)

// TestServeEndToEnd builds the real binary and drives it the way an operator would:
// init → users/keys/tokens/repos via the CLI → `amcd serve` → git over SSH and HTTPS with
// the real hook scripts (written by serve, pointing at the built binary) → revoke → stop.
func TestServeEndToEnd(t *testing.T) {
	for _, b := range []string{"git", "ssh", "go"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	inst := filepath.Join(dir, "inst")
	_ = os.MkdirAll(inst, 0o700)
	cfgPath := filepath.Join(inst, "config.toml")
	cfg := config.Default()
	var err error
	cfg.Server.HTTPAddr, cfg.Server.SSHAddr = "127.0.0.1:0", "127.0.0.1:0"
	cfg.Auth.Argon2MemoryKiB = 8 * 1024 // fast test hashing
	if cfg.Auth.SecretKey, err = secret.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := config.Write(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cli := func(stdin string, wantCode int, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"--config", cfgPath}, args...)...)
		cmd.Env = append(os.Environ(), "CODR_LANG=en-US", "HOME="+home)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != wantCode {
			t.Fatalf("amcd %v: code %d want %d\n%s", args, code, wantCode, out)
		}
		return string(out)
	}
	cli("", 0, "migrate")
	cli("correct horse battery staple\n", 0, "admin", "user", "add", "alice", "--lang", "en-US")
	cli("another long password here\n", 0, "admin", "user", "add", "bob", "--lang", "ms-MY")
	// SSH key for alice
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	keyFile := filepath.Join(dir, "alice.key")
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)
	cli(string(ssh.MarshalAuthorizedKey(sshPub)), 0, "admin", "key", "add", "alice", "--title", "laptop")
	// token (shown once); scopes and days are validated
	cli("", 2, "admin", "token", "add", "alice", "--scopes", "repo:everything")
	cli("", 2, "admin", "token", "add", "alice", "--days", "9999")
	out := cli("", 0, "admin", "token", "add", "alice", "--name", "laptop")
	if strings.Contains(out, "%!") {
		t.Fatalf("token add message has a formatting hole: %s", out)
	}
	m := regexp.MustCompile(`(?m)^  (codr_[0-9a-f]{64})$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("token not printed once on its own line:\n%s", out)
	}
	token := m[1]
	bobTok := regexp.MustCompile(`codr_[0-9a-f]{64}`).FindString(cli("", 0, "admin", "token", "add", "bob"))
	cli("", 0, "admin", "repo", "add", "alice/p")
	if out := cli("", 0, "admin", "protect", "ls", "alice/p"); !strings.Contains(out, "default branch main protected by setting repo.protect_default_branch=true") {
		t.Fatalf("protect ls: %s", out)
	}
	cli("", 0, "admin", "protect", "add", "alice/p", "release/*", "--allow-force")
	cli("", 2, "admin", "protect", "add", "alice/p", "[")
	if out := cli("", 0, "admin", "setting", "ls"); !strings.Contains(out, "repo.push_max_mb = 500") {
		t.Fatalf("setting ls: %s", out)
	}
	cli("", 2, "admin", "setting", "set", "repo.push_max_mb", "lots")
	cli("", 2, "admin", "setting", "set", "nope.key", "1")
	cli("", 0, "admin", "setting", "set", "repo.push_max_mb", "1")

	if out := cli("", 0, "doctor"); !strings.Contains(out, "is not running") || !strings.Contains(out, "hook scripts absent") || strings.Contains(out, "FAIL") {
		t.Fatalf("doctor before serve: %s", out)
	}
	// serve
	srv := exec.Command(bin, "--config", cfgPath, "serve")
	srv.Env = append(os.Environ(), "CODR_LANG=en-US", "HOME="+home)
	stdout, _ := srv.StdoutPipe()
	var stderr bytes.Buffer
	srv.Stderr = &stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill() })
	var sshAddr, httpAddr string
	sc := bufio.NewScanner(stdout)
	re := regexp.MustCompile(`listening on (127\.0\.0\.1:\d+)`)
	for (sshAddr == "" || httpAddr == "") && sc.Scan() {
		line := sc.Text()
		if m := re.FindStringSubmatch(line); m != nil {
			if strings.HasPrefix(line, "SSH") {
				sshAddr = m[1]
			} else {
				httpAddr = m[1]
			}
		}
	}
	if sshAddr == "" || httpAddr == "" {
		t.Fatalf("serve did not announce both doors: %s", stderr.String())
	}
	go func() { _, _ = io.Copy(io.Discard, stdout) }()
	if _, err := os.Stat(filepath.Join(inst, "data", "hooks", "pre-receive")); err != nil {
		t.Fatal("serve did not write hook scripts")
	}
	git := func(dir string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "HOME="+home, "GIT_TERMINAL_PROMPT=0",
			"GIT_SSH_COMMAND=ssh -i "+keyFile+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR")
		var b bytes.Buffer
		cmd.Stdout, cmd.Stderr = &b, &b
		err := cmd.Run()
		return b.String(), err
	}
	host, port, _ := net.SplitHostPort(sshAddr)
	sshURL := "ssh://git@" + host + ":" + port + "/alice/p.git"
	httpURL := func(user, tok string) string {
		u, _ := url.Parse("http://" + httpAddr + "/alice/p.git")
		if user != "" {
			u.User = url.UserPassword(user, tok)
		}
		return u.String()
	}
	work := filepath.Join(dir, "work")
	_ = os.MkdirAll(work, 0o700)
	if o, err := git(work, "init", "-q"); err != nil {
		t.Fatal(o)
	}
	commit := func(name, content string) string {
		_ = os.WriteFile(filepath.Join(work, name), []byte(content), 0o600)
		if o, err := git(work, "add", "."); err != nil {
			t.Fatal(o)
		}
		if o, err := git(work, "commit", "-q", "-m", name); err != nil {
			t.Fatal(o)
		}
		o, _ := git(work, "rev-parse", "HEAD")
		return strings.TrimSpace(o)
	}
	first := commit("a", "1")
	if o, err := git(work, "push", "-q", httpURL("alice", token), "main"); err != nil {
		t.Fatalf("https push: %v\n%s\n%s", err, o, stderr.String())
	}
	want := commit("b", "2")
	if o, err := git(work, "push", "-q", sshURL, "main"); err != nil {
		t.Fatalf("ssh push: %v\n%s\n%s", err, o, stderr.String())
	}
	clone := filepath.Join(dir, "clone")
	if o, err := git(dir, "clone", "-q", httpURL("alice", token), clone); err != nil {
		t.Fatalf("https clone: %v %s", err, o)
	}
	if o, _ := git(clone, "rev-parse", "HEAD"); strings.TrimSpace(o) != want {
		t.Fatalf("clone HEAD %s != %s", o, want)
	}
	// bob: private repo is not found at the HTTP door even with a valid token
	if o, err := git(dir, "clone", "-q", httpURL("bob", bobTok), filepath.Join(dir, "bobc")); err == nil || !strings.Contains(o, "not found") {
		t.Fatalf("bob clone: %v %s", err, o)
	}
	// hooks: force push to main refused at both doors with alice's language (en-US)
	if o, err := git(work, "reset", "-q", "--hard", first); err != nil {
		t.Fatal(o)
	}
	commit("c", "3")
	for _, u := range []string{sshURL, httpURL("alice", token)} {
		if o, err := git(work, "push", "-q", "--force", u, "main"); err == nil || !strings.Contains(o, "branch main is protected on alice/p") {
			t.Fatalf("force push via %s: %v %s", u, err, o)
		}
	}
	// release/* carries allow_force: non-ff push there is fine
	if o, err := git(work, "push", "-q", httpURL("alice", token), "main:release/1"); err != nil {
		t.Fatal(o)
	}
	// oversize (limit set to 1 MB above) refused with the limit named
	blob := make([]byte, 2<<20)
	_, _ = rand.Read(blob)
	commit("big.bin", string(blob))
	if o, err := git(work, "push", "-q", httpURL("alice", token), "main:big"); err == nil || !strings.Contains(o, "limit 1 MB") {
		t.Fatalf("oversize push: %v %s", err, o)
	}
	// import: a mirror of a local bare repo (CLI may use file://), refreshed on demand, read-only at both doors
	srcBare := filepath.Join(dir, "src.git")
	if o, err := git(dir, "clone", "-q", "--bare", work, srcBare); err != nil {
		t.Fatal(o)
	}
	out = cli("", 0, "admin", "repo", "import", "alice/cermin", "file://"+srcBare, "--interval", "5")
	if !strings.Contains(out, "mirror alice/cermin is fresh (default branch: main)") {
		t.Fatalf("import: %s", out)
	}
	if out := cli("", 0, "admin", "mirror", "ls"); !strings.Contains(out, "alice/cermin  file://") || !strings.Contains(out, "every 5 min") || !strings.Contains(out, " ok") {
		t.Fatalf("mirror ls: %s", out)
	}
	cermin := filepath.Join(dir, "cermin")
	if o, err := git(dir, "clone", "-q", "ssh://git@"+host+":"+port+"/alice/cermin.git", cermin); err != nil {
		t.Fatalf("clone mirror: %v %s", err, o)
	}
	srcHead, _ := git(work, "rev-parse", "HEAD") // work has moved on since `want`: the mirror must match the SOURCE now
	if o, _ := git(cermin, "rev-parse", "HEAD"); strings.TrimSpace(o) != strings.TrimSpace(srcHead) {
		t.Fatalf("mirror HEAD %s != source %s", o, srcHead)
	}
	if o, err := git(work, "push", "-q", "ssh://git@"+host+":"+port+"/alice/cermin.git", "main:x"); err == nil || !strings.Contains(o, "is a mirror") {
		t.Fatalf("push to mirror via ssh must be refused: %v %s", err, o)
	}
	if o, err := git(work, "push", "-q", strings.Replace(httpURL("alice", token), "/alice/p.git", "/alice/cermin.git", 1), "main:x"); err == nil || !strings.Contains(o, "403") {
		t.Fatalf("push to mirror via https must be refused: %v %s", err, o)
	}
	cli("", 2, "admin", "mirror", "sync", "alice/p")
	cli("", 0, "admin", "mirror", "sync", "--all")
	cli("", 0, "admin", "mirror", "rm", "alice/cermin")
	if o, err := git(work, "push", "-q", "ssh://git@"+host+":"+port+"/alice/cermin.git", "main:x"); err != nil {
		t.Fatalf("push after mirror rm: %v %s", err, o)
	}
	// doctor while serve runs: all green, no failures
	if out := cli("", 0, "doctor"); !strings.Contains(out, "serve is running") || !strings.Contains(out, "all ") || strings.Contains(out, "FAIL") {
		t.Fatalf("doctor: %s", out)
	}
	// a second serve on the same data dir stops early and leaves the live hook socket alone (5 Nov 2026)
	if out := cli("", 1, "serve"); !strings.Contains(out, "already running on hook socket") {
		t.Fatalf("second serve: %s", out)
	}
	if o, err := git(work, "push", "-q", "ssh://git@"+host+":"+port+"/alice/cermin.git", "main:y"); err != nil {
		t.Fatalf("push after a second serve was refused: %v %s", err, o)
	}
	// revoke the token: the next push is 401
	ls := cli("", 0, "admin", "token", "ls", "alice")
	id := regexp.MustCompile(`(?m)^(\d+)  laptop`).FindStringSubmatch(ls)
	if id == nil || !strings.Contains(ls, "last used 20") {
		t.Fatalf("token ls: %s", ls)
	}
	cli("", 0, "admin", "token", "rm", "alice", id[1])
	if o, err := git(work, "push", "-q", httpURL("alice", token), "main:after"); err == nil || !strings.Contains(o, "Authentication failed") {
		t.Fatalf("revoked token push: %v %s", err, o)
	}
	// audit rows name the door (written by the post-receive hook, one place for both)
	st, err := store.Open(filepath.Join(inst, "data", "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for door, want := range map[string]int{"ssh": 1, "https": 2} {
		var n int
		if err := st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action='git.push' AND target='alice/p' AND detail LIKE ?`, door+" %").Scan(&n); err != nil || n != want {
			t.Fatalf("audit %s pushes = %d (%v), want %d", door, n, err, want)
		}
	}
	// stop cleanly
	if err := srv.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve exit: %v\n%s", err, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop on SIGTERM")
	}
	if _, err := os.Stat(filepath.Join(inst, "data", "hook.sock")); err == nil {
		t.Fatal("hook socket left behind")
	}
}

// TestBackupRestore: backup → restore into an empty dir → doctor green → every repo's HEAD equals
// the manifest and the original; rows (users, tokens, mirrors) survive.
func TestBackupRestore(t *testing.T) {
	for _, b := range []string{"git", "go"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codr")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	cli := func(cfg, stdin string, wantCode int, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, append([]string{"--config", cfg}, args...)...)
		cmd.Env = append(os.Environ(), "CODR_LANG=en-US", "HOME="+home)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		if code != wantCode {
			t.Fatalf("amcd %v: code %d want %d\n%s", args, code, wantCode, out)
		}
		return string(out)
	}
	inst := filepath.Join(dir, "inst")
	cfg := filepath.Join(inst, "config.toml")
	cli("", "", 0, "init", "--dir", inst)
	cli(cfg, "correct horse battery staple\n", 0, "admin", "user", "add", "alice")
	cli(cfg, "", 0, "admin", "token", "add", "alice")
	// recovery from the server (D80): a new password and 2FA off, both signing the person out everywhere
	if out := cli(cfg, "another long password\n", 0, "admin", "user", "passwd", "alice"); !strings.Contains(out, "password changed for alice; 0 sessions signed out") {
		t.Fatalf("passwd: %s", out)
	}
	cli(cfg, "short\n", 2, "admin", "user", "passwd", "alice")
	cli(cfg, "x\n", 2, "admin", "user", "passwd", "nobody")
	if out := cli(cfg, "", 0, "admin", "user", "2fa-off", "alice"); !strings.Contains(out, "two-step sign-in switched off for alice") {
		t.Fatalf("2fa-off: %s", out)
	}
	cli(cfg, "", 0, "admin", "repo", "add", "alice/kosong")
	cli(cfg, "", 0, "admin", "repo", "add", "alice/p")
	// put a commit into alice/p straight on disk (D13: bare repos are plain git)
	ls := cli(cfg, "", 0, "admin", "repo", "ls", "alice")
	var pPath string
	for _, line := range strings.Split(ls, "\n") {
		if strings.Contains(line, "alice/p ") {
			f := strings.Fields(line)
			pPath = f[len(f)-1]
		}
	}
	work := filepath.Join(dir, "work")
	_ = os.MkdirAll(work, 0o700)
	g := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-C", work}, args...)...)
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	g("init", "-q")
	_ = os.WriteFile(filepath.Join(work, "a"), []byte("1\n"), 0o600)
	g("add", ".")
	g("commit", "-q", "-m", "satu")
	g("tag", "v1")
	g("push", "-q", pPath, "main", "v1")
	want := g("rev-parse", "HEAD")
	// a mirror repo (source = the work tree over file://): listed in the manifest, not bundled (D54)
	cli(cfg, "", 0, "admin", "repo", "import", "alice/cermin", "file://"+work, "--interval", "5")

	// one Git LFS object in alice/p (D82): it must travel through backup and restore, byte for byte
	pID, _ := strconv.ParseInt(strings.TrimSuffix(filepath.Base(pPath), ".git"), 10, 64)
	lfsData := "objek LFS untuk backup"
	lfsSum := sha256.Sum256([]byte(lfsData))
	lfsOID := hex.EncodeToString(lfsSum[:])
	if err := (lfs.Store{Root: filepath.Join(inst, "data", "lfs")}).Put(pID, lfsOID, int64(len(lfsData)), 0, strings.NewReader(lfsData)); err != nil {
		t.Fatal(err)
	}
	// a wiki page (D83): bundled and restored with its head
	wk := wiki.Wiki{Path: filepath.Join(inst, "data", "repos", strconv.FormatInt(pID, 10)+".wiki.git"), Git: "git", Timeout: 30 * time.Second}
	wikiHead, err := wk.Save(context.Background(), wiki.Home, "", []byte("wiki untuk backup"), wiki.Author{Name: "alice", Email: "a@x.invalid"}, "Home", "")
	if err != nil {
		t.Fatal(err)
	}
	out := cli(cfg, "", 0, "backup", filepath.Join(dir, "bk"))
	if !strings.Contains(out, "3 repos, schema") || !strings.Contains(out, "1 mirror repos listed without a bundle") {
		t.Fatalf("backup: %s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "bk")); len(entries) == 1 {
		bundles, _ := os.ReadDir(filepath.Join(dir, "bk", entries[0].Name(), "repos"))
		if len(bundles) != 2 || !strings.HasSuffix(bundles[0].Name()+" "+bundles[1].Name(), ".bundle") { // alice/p and its wiki: kosong has no commits, cermin is a mirror
			t.Fatalf("bundles: %v", bundles)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "bk"))
	bk := filepath.Join(dir, "bk", entries[0].Name())
	for _, f := range []string{"codr.db", "config.toml", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(bk, f)); err != nil {
			t.Fatalf("backup lacks %s", f)
		}
	}
	restored := filepath.Join(dir, "restored")
	_ = os.MkdirAll(restored, 0o700)
	if out := cli(cfg, "", 0, "restore", bk, "--dir", restored); !strings.Contains(out, "1 mirror repos restored EMPTY") {
		t.Fatalf("restore: %s", out)
	}
	if got, err := os.ReadFile((lfs.Store{Root: filepath.Join(restored, "data", "lfs")}).Path(pID, lfsOID)); err != nil || string(got) != lfsData {
		t.Fatalf("LFS object not restored: %v %q", err, got)
	}
	rw := wiki.Wiki{Path: filepath.Join(restored, "data", "repos", strconv.FormatInt(pID, 10)+".wiki.git"), Git: "git", Timeout: 30 * time.Second}
	if h, _ := rw.Head(context.Background()); h != wikiHead {
		t.Fatalf("wiki head after restore: %q want %q", h, wikiHead)
	}
	if b, _, _ := rw.Read(context.Background(), wiki.Home, ""); string(b) != "wiki untuk backup" {
		t.Fatalf("wiki page after restore: %q", b)
	}
	// a damaged LFS object in a backup fails the restore by name (the backup is copied first: its files are hard links)
	damaged := filepath.Join(dir, "rosak")
	if out, err := exec.Command("cp", "-r", bk, damaged).CombinedOutput(); err != nil {
		t.Fatalf("cp: %s", out)
	}
	_ = os.WriteFile((lfs.Store{Root: filepath.Join(damaged, "lfs")}).Path(pID, lfsOID), []byte("rosak"), 0o600)
	if out := cli(cfg, "", 1, "restore", damaged, "--dir", filepath.Join(dir, "restored-rosak")); !strings.Contains(out, "LFS objects of repository") {
		t.Fatalf("damaged LFS restore: %s", out)
	}
	rcfg := filepath.Join(restored, "config.toml")
	if out := cli(rcfg, "", 0, "doctor"); strings.Contains(out, "FAIL") || !strings.Contains(out, "3 repos in DB, all present on disk") {
		t.Fatalf("doctor after restore: %s", out)
	}
	// the restored mirror is empty until synced from its source URL, then equals the source
	if out := cli(rcfg, "", 0, "admin", "mirror", "sync", "--all"); !strings.Contains(out, "alice/cermin is fresh") {
		t.Fatalf("mirror sync after restore: %s", out)
	}
	if out := cli(rcfg, "", 0, "admin", "repo", "ls", "alice"); !strings.Contains(out, "alice/cermin") {
		t.Fatalf("mirror row lost: %s", out)
	}
	rls := cli(rcfg, "", 0, "admin", "repo", "ls", "alice")
	var rp string
	for _, line := range strings.Split(rls, "\n") {
		if strings.Contains(line, "alice/p ") {
			f := strings.Fields(line)
			rp = f[len(f)-1]
		}
	}
	if rp == "" || rp == pPath {
		t.Fatalf("restored path: %q", rp)
	}
	cmd := exec.Command("git", "-C", rp, "rev-parse", "HEAD")
	got, _ := cmd.Output()
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("restored HEAD %s != %s", got, want)
	}
	cmd = exec.Command("git", "-C", rp, "tag")
	tags, _ := cmd.Output()
	if strings.TrimSpace(string(tags)) != "v1" {
		t.Fatalf("tags: %q", tags)
	}
	if out := cli(rcfg, "", 0, "admin", "token", "ls", "alice"); !strings.Contains(out, "cli 20") {
		t.Fatalf("token row lost: %s", out)
	}
	// --with-mirrors bundles the mirror too
	if out := cli(cfg, "", 0, "backup", "--with-mirrors", filepath.Join(dir, "bk3")); strings.Contains(out, "without a bundle") {
		t.Fatalf("with-mirrors: %s", out)
	}
	// a repo row whose folder vanished must refuse the backup, never produce an "empty" one
	if err := os.RemoveAll(pPath); err != nil {
		t.Fatal(err)
	}
	if out := cli(cfg, "", 1, "backup", filepath.Join(dir, "bk2")); !strings.Contains(out, "missing on disk") {
		t.Fatalf("backup with missing repo dir: %s", out)
	}
	if left, _ := os.ReadDir(filepath.Join(dir, "bk2")); len(left) != 0 {
		t.Fatalf("failed backup left a folder behind: %v", left)
	}
	// refuses a non-empty target and a non-backup source
	cli(cfg, "", 2, "restore", bk, "--dir", restored)
	cli(cfg, "", 2, "restore", dir, "--dir", filepath.Join(dir, "x"))
	// a schema older than the binary (deploy backs up BEFORE migrating) is refused by name, not with a bare SQL
	// error half-way through (v2.13.0: "no such column: r.fork_of")
	st, err := store.Open(filepath.Join(inst, "data", "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if out := cli(cfg, "", 1, "backup", filepath.Join(dir, "bk4")); !strings.Contains(out, "not yet applied") {
		t.Fatalf("backup on an older schema: %s", out)
	}
}
