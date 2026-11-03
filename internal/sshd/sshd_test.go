package sshd

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/hooks"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/wiki"
)

// TestMain lets git run this test binary as `amcd hook <name>` (D9) so the real hook
// path — script → socket → serve — is exercised, not a stub.
func TestMain(m *testing.M) {
	hooks.RunIfHook(os.Args)
	os.Exit(m.Run())
}

type harness struct {
	t     *testing.T
	st    *store.Store
	disk  repos.Disk
	addr  string
	users map[string]store.User
	keys  map[string]ssh.Signer
	files map[string]string // private key files for the ssh binary
	srv   *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	for _, bin := range []string{"git", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st, disk: repos.Disk{Root: filepath.Join(dir, "repos"), GitBinary: "git"},
		users: map[string]store.User{}, keys: map[string]ssh.Signer{}, files: map[string]string{}}
	for _, name := range []string{"alice", "bob"} {
		u, err := st.CreateUser(context.Background(), name, "", "x", false, "en-US")
		if err != nil {
			t.Fatal(err)
		}
		h.users[name] = u
		pub, priv, _ := ed25519.GenerateKey(rand.Reader)
		signer, _ := ssh.NewSignerFromKey(priv)
		h.keys[name] = signer
		sshPub, _ := ssh.NewPublicKey(pub)
		if _, err := st.AddSSHKey(context.Background(), u.ID, name+"-key", ssh.FingerprintSHA256(sshPub), string(ssh.MarshalAuthorizedKey(sshPub))); err != nil {
			t.Fatal(err)
		}
		block, _ := ssh.MarshalPrivateKey(priv, "")
		p := filepath.Join(dir, name+".key")
		if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
		h.files[name] = p
	}
	hostKey, err := LoadOrCreateHostKey(filepath.Join(dir, "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	hooksDir, sock := filepath.Join(dir, "hooks"), filepath.Join(dir, "hook.sock")
	self, _ := os.Executable()
	if err := hooks.WriteScripts(hooksDir, self); err != nil {
		t.Fatal(err)
	}
	hln, err := hooks.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	hsrv := &hooks.Server{Store: st, Disk: h.disk, GitBinary: "git", Log: logger}
	srv := &Server{Store: st, Disk: h.disk, Git: gitcmd.Runner{Binary: "git", Timeout: 30 * time.Second, HooksDir: hooksDir, HookSocket: sock},
		HostKey: hostKey, MaxAuthTries: 3, ExecWait: 10 * time.Second, Log: logger}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.addr = ln.Addr().String()
	h.srv = srv
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = srv.Serve(ctx, ln); done <- struct{}{} }()
	go func() { _ = hsrv.Serve(ctx, hln); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
	return h
}

func (h *harness) repo(owner, name string, private bool) store.Repo {
	h.t.Helper()
	r, err := h.st.CreateRepo(context.Background(), h.users[owner].ID, name, "", private, "main")
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.disk.Create(context.Background(), r.ID, "main"); err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) url(ref string) string {
	host, port, _ := net.SplitHostPort(h.addr)
	return fmt.Sprintf("ssh://git@%s:%s/%s.git", host, port, ref)
}

// git runs git as `user` with GIT_SSH_COMMAND pointing at that user's key.
func (h *harness) git(user, dir string, args ...string) (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+h.files[user]+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o LogLevel=ERROR",
		"GIT_TERMINAL_PROMPT=0", "HOME="+h.t.TempDir())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (h *harness) commit(dir, file, content string) string {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "."}, {"commit", "-q", "-m", "c " + file}} {
		if out, err := h.git("alice", dir, a...); err != nil {
			h.t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	out, _ := h.git("alice", dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

func TestClonePushCloneRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.repo("alice", "proj", true)
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("alice", work, "init", "-q"); err != nil {
		t.Fatalf("init: %v %s", err, out)
	}
	want := h.commit(work, "README.md", "# proj\n")
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/proj"), "main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	if out, err := h.git("alice", t.TempDir(), "clone", "-q", h.url("alice/proj"), clone); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	got, _ := h.git("alice", clone, "rev-parse", "HEAD")
	if strings.TrimSpace(got) != want {
		t.Fatalf("clone HEAD %s != pushed %s", got, want)
	}
	// second push (fetch + push over an existing repo) and the audit trail
	want2 := h.commit(work, "b.txt", "b\n")
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/proj"), "main"); err != nil {
		t.Fatalf("push 2: %v\n%s", err, out)
	}
	if out, err := h.git("alice", clone, "pull", "-q", h.url("alice/proj"), "main"); err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	got, _ = h.git("alice", clone, "rev-parse", "HEAD")
	if strings.TrimSpace(got) != want2 {
		t.Fatalf("pull HEAD %s != %s", got, want2)
	}
	var pushes int
	if err := h.st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'git.push' AND target = 'alice/proj'`).Scan(&pushes); err != nil || pushes != 2 {
		t.Fatalf("audit pushes = %d %v", pushes, err)
	}
}

func TestOtherUserIsRefusedAtEveryStep(t *testing.T) {
	h := newHarness(t)
	r := h.repo("alice", "secret", true)
	work := filepath.Join(t.TempDir(), "work")
	_ = os.MkdirAll(work, 0o700)
	_, _ = h.git("alice", work, "init", "-q")
	h.commit(work, "f", "x")
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/secret"), "main"); err != nil {
		t.Fatalf("owner push: %v %s", err, out)
	}
	// bob: private repo is invisible
	if out, err := h.git("bob", t.TempDir(), "clone", "-q", h.url("alice/secret"), "x"); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("bob clone must fail with not-found, got err=%v out=%s", err, out)
	}
	if out, err := h.git("bob", work, "push", "-q", h.url("alice/secret"), "main"); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("bob push must fail with not-found, got err=%v out=%s", err, out)
	}
	// bob as reader: clone ok, push refused with a *write* message (repo known to him)
	if err := h.st.SetCollaborator(context.Background(), r.ID, h.users["bob"].ID, store.AccessRead); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("bob", t.TempDir(), "clone", "-q", h.url("alice/secret"), "y"); err != nil {
		t.Fatalf("bob reader clone: %v %s", err, out)
	}
	h.commit(work, "g", "y")
	if out, err := h.git("bob", work, "push", "-q", h.url("alice/secret"), "main"); err == nil || !strings.Contains(out, "write") {
		t.Fatalf("bob reader push must be refused, got err=%v out=%s", err, out)
	}
	// archived: even the owner cannot push, but can still clone
	if err := h.st.SetArchived(context.Background(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/secret"), "main"); err == nil || !strings.Contains(out, "archived") {
		t.Fatalf("archived push must be refused, got err=%v out=%s", err, out)
	}
	if out, err := h.git("alice", t.TempDir(), "clone", "-q", h.url("alice/secret"), "z"); err != nil {
		t.Fatalf("archived clone must work: %v %s", err, out)
	}
	// unknown repo and reserved owner names are indistinguishable from private ones
	for _, ref := range []string{"alice/nope", "nobody/proj", "admin/x"} {
		if out, err := h.git("alice", t.TempDir(), "clone", "-q", h.url(ref), "w"); err == nil || !strings.Contains(out, "Codr:") {
			t.Fatalf("clone %s must fail with an AMCD message, got err=%v out=%s", ref, err, out)
		}
	}
}

func dial(t *testing.T, h *harness, auth ssh.AuthMethod) (*ssh.Client, error) {
	t.Helper()
	return ssh.Dial("tcp", h.addr, &ssh.ClientConfig{User: "git", Auth: []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}) // #nosec G106 -- test against an in-process server
}

func TestNoShellNoPtyNoForwardNoPassword(t *testing.T) {
	h := newHarness(t)
	if _, err := dial(t, h, ssh.Password("hunter2")); err == nil {
		t.Fatal("password auth must be refused")
	}
	_, stray, _ := ed25519.GenerateKey(rand.Reader)
	straySigner, _ := ssh.NewSignerFromKey(stray)
	if _, err := dial(t, h, ssh.PublicKeys(straySigner)); err == nil {
		t.Fatal("unregistered key must be refused")
	}
	c, err := dial(t, h, ssh.PublicKeys(h.keys["alice"]))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// arbitrary command
	s, _ := c.NewSession()
	var stderr bytes.Buffer
	s.Stderr = &stderr
	if err := s.Run("ls /"); err == nil || !strings.Contains(stderr.String(), "Codr:") {
		t.Fatalf("ls must be refused with an AMCD line: err=%v stderr=%q", err, stderr.String())
	}
	s.Close()
	// shell
	s, _ = c.NewSession()
	if err := s.Shell(); err == nil {
		t.Fatal("shell must be refused")
	}
	s.Close()
	// pty
	s, _ = c.NewSession()
	if err := s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err == nil {
		t.Fatal("pty must be refused")
	}
	s.Close()
	// subsystem (sftp)
	s, _ = c.NewSession()
	if err := s.RequestSubsystem("sftp"); err == nil {
		t.Fatal("subsystem must be refused")
	}
	s.Close()
	// direct-tcpip channel
	if _, err := c.Dial("tcp", "127.0.0.1:22"); err == nil {
		t.Fatal("port forwarding must be refused")
	}
	// remote forward (global request)
	if ok, _, _ := c.SendRequest("tcpip-forward", true, ssh.Marshal(struct {
		Addr string
		Port uint32
	}{"127.0.0.1", 0})); ok {
		t.Fatal("tcpip-forward must be refused")
	}
	// env other than GIT_PROTOCOL is refused; GIT_PROTOCOL accepted
	s, _ = c.NewSession()
	if err := s.Setenv("LD_PRELOAD", "/x"); err == nil {
		t.Fatal("LD_PRELOAD env must be refused")
	}
	if err := s.Setenv("GIT_PROTOCOL", "version=2"); err != nil {
		t.Fatalf("GIT_PROTOCOL env must be accepted: %v", err)
	}
	if err := s.Setenv("GIT_PROTOCOL", "version=2;evil"); err == nil {
		t.Fatal("malformed GIT_PROTOCOL must be refused")
	}
	s.Close()
}

// TestHooksGuardProtectedBranchAndSize: the pre-receive hook (shared by every door) refuses a
// force push and a deletion of the default branch, and a push above repo.push_max_mb — each with
// the user's language. The audit row only exists for the accepted push.
func TestHooksGuardProtectedBranchAndSize(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	r := h.repo("alice", "guard", true)
	work := filepath.Join(t.TempDir(), "work")
	_ = os.MkdirAll(work, 0o700)
	_, _ = h.git("alice", work, "init", "-q")
	first := h.commit(work, "a", "1")
	h.commit(work, "b", "2")
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/guard"), "main"); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}
	// alice speaks en-US (harness); a non-fast-forward push to main is refused by name
	if out, err := h.git("alice", work, "reset", "-q", "--hard", first); err != nil {
		t.Fatal(out)
	}
	h.commit(work, "c", "3")
	out, err := h.git("alice", work, "push", "-q", "--force", h.url("alice/guard"), "main")
	if err == nil || !strings.Contains(out, "branch main is protected on alice/guard") {
		t.Fatalf("force push must be refused with the EN message, got err=%v out=%s", err, out)
	}
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/guard"), ":main"); err == nil || !strings.Contains(out, "cannot be deleted") {
		t.Fatalf("delete must be refused, got err=%v out=%s", err, out)
	}
	// an unprotected branch may be force-pushed and deleted
	for _, a := range [][]string{{"push", "-q", h.url("alice/guard"), "main:scratch"}, {"push", "-q", "--force", h.url("alice/guard"), "main:scratch"}, {"push", "-q", h.url("alice/guard"), ":scratch"}} {
		if out, err := h.git("alice", work, a...); err != nil {
			t.Fatalf("%v: %v %s", a, err, out)
		}
	}
	// a pattern rule with allow_force lets the force through; the setting turns main off entirely
	if err := h.st.Protect(ctx, r.ID, "main", true, false, false); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("alice", work, "push", "-q", "--force", h.url("alice/guard"), "main"); err != nil {
		t.Fatalf("allow_force rule ignored: %v %s", err, out)
	}
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/guard"), ":main"); err == nil || !strings.Contains(out, "cannot be deleted") {
		t.Fatalf("delete must still be refused under allow_force-only rule, got err=%v out=%s", err, out)
	}
	// size limit, in Malay for a Malay user
	if _, err := h.st.DB().Exec(`UPDATE users SET lang='ms-MY' WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSetting(ctx, "repo.push_max_mb", "1", 0); err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 2<<20)
	if _, err := rand.Read(blob); err != nil {
		t.Fatal(err)
	}
	h.commit(work, "big.bin", string(blob))
	out, err = h.git("alice", work, "push", "-q", h.url("alice/guard"), "main:big")
	if err == nil || !strings.Contains(out, "melebihi") && !strings.Contains(out, "had 1 MB") {
		t.Fatalf("oversize push must be refused in Malay, got err=%v out=%s", err, out)
	}
	var pushes int
	_ = h.st.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='git.push' AND target='alice/guard'`).Scan(&pushes)
	if pushes != 4 { // first push, main:scratch, delete scratch, allowed force on main (force scratch was a no-op: no receive-pack)
		t.Fatalf("audit rows = %d, want 4 (refused pushes must not be audited as pushes)", pushes)
	}
}

// TestOrgRulesOverSSH: the organisation rules hold at the SSH door too — a stranger gets "not found", a member
// with base read clones but cannot push, a team with write pushes (D74).
func TestOrgRulesOverSSH(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	org, err := h.st.CreateOrg(ctx, "syarikat", h.users["alice"].ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.st.CreateRepo(ctx, org.ID, "rahsia", "", true, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.disk.Create(ctx, r.ID, "main"); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work")
	_ = os.MkdirAll(work, 0o700)
	_, _ = h.git("alice", work, "init", "-q")
	h.commit(work, "f", "x")
	if out, err := h.git("alice", work, "push", "-q", h.url("syarikat/rahsia"), "main"); err != nil {
		t.Fatalf("org owner push: %v %s", err, out)
	}
	if out, err := h.git("bob", t.TempDir(), "clone", "-q", h.url("syarikat/rahsia"), "x"); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("stranger clone must be not-found: %v %s", err, out)
	}
	if err := h.st.SetOrgMember(ctx, org.ID, h.users["bob"].ID, "member"); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("bob", t.TempDir(), "clone", "-q", h.url("syarikat/rahsia"), "y"); err != nil {
		t.Fatalf("member clone: %v %s", err, out)
	}
	h.commit(work, "g", "y")
	if out, err := h.git("bob", work, "push", "-q", h.url("syarikat/rahsia"), "main"); err == nil || !strings.Contains(out, "write") {
		t.Fatalf("member with base read pushed: %v %s", err, out)
	}
	team, _ := h.st.CreateTeam(ctx, org.ID, "pembangun")
	_ = h.st.SetTeamMember(ctx, org.ID, team, h.users["bob"].ID)
	if err := h.st.SetTeamRepo(ctx, org.ID, team, r.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git("bob", work, "push", "-q", h.url("syarikat/rahsia"), "main"); err != nil {
		t.Fatalf("team writer push over SSH: %v %s", err, out)
	}
}

// TestCodrRefsHiddenOverSSH: refs/codr/ (pull request heads from forks) is neither listed nor writable over SSH (D75).
func TestCodrRefsHiddenOverSSH(t *testing.T) {
	h := newHarness(t)
	r := h.repo("alice", "proj", true)
	work := filepath.Join(t.TempDir(), "work")
	_ = os.MkdirAll(work, 0o700)
	_, _ = h.git("alice", work, "init", "-q")
	h.commit(work, "f", "x")
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/proj"), "main"); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}
	// a Codr ref written on disk, as a pull request from a fork would
	cmd := exec.Command("git", "--git-dir", h.disk.Path(r.ID), "update-ref", "refs/codr/pull/1", "refs/heads/main")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("update-ref: %v %s", err, out)
	}
	if out, _ := h.git("alice", work, "ls-remote", h.url("alice/proj")); strings.Contains(out, "refs/codr/") || !strings.Contains(out, "refs/heads/main") {
		t.Fatalf("ls-remote over SSH: %s", out)
	}
	if out, err := h.git("alice", work, "push", "-q", h.url("alice/proj"), "HEAD:refs/codr/pull/2"); err == nil {
		t.Fatalf("push to refs/codr over SSH accepted: %s", out)
	}
}

// git-lfs-authenticate (D82) is answered by the LFSAuth callback with the person's id and the parsed request; it runs
// no program, and every other shape of the line is still refused.
func TestLFSAuthenticateOverSSH(t *testing.T) {
	h := newHarness(t)
	var gotUID int64
	var gotRef, gotOp string
	h.srv.LFSAuth = func(_ context.Context, uid int64, owner, repo, op string) ([]byte, error) {
		gotUID, gotRef, gotOp = uid, owner+"/"+repo, op
		if repo == "tiada" {
			return nil, store.ErrNotFound
		}
		return []byte(`{"href":"https://forge/x.git/info/lfs"}`), nil
	}
	c, err := dial(t, h, ssh.PublicKeys(h.keys["alice"]))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(cmd string) (string, string, error) {
		s, _ := c.NewSession()
		defer s.Close()
		var out, errb bytes.Buffer
		s.Stdout, s.Stderr = &out, &errb
		err := s.Run(cmd)
		return out.String(), errb.String(), err
	}
	out, _, err := run("git-lfs-authenticate 'alice/proj.git' download")
	if err != nil || !strings.Contains(out, `"href":"https://forge/x.git/info/lfs"`) || gotUID != h.users["alice"].ID || gotRef != "alice/proj" || gotOp != "download" {
		t.Fatalf("lfs-authenticate: %v %q uid=%d %s %s", err, out, gotUID, gotRef, gotOp)
	}
	if _, stderr, err := run("git-lfs-authenticate alice/tiada upload"); err == nil || !strings.Contains(stderr, "Codr:") {
		t.Fatalf("refused repository: %v %q", err, stderr)
	}
	for _, bad := range []string{"git-lfs-authenticate alice/proj delete", "git-lfs-authenticate alice/proj upload; id", "git-lfs-transfer alice/proj upload"} {
		if _, _, err := run(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

// The wiki (D83) clones over SSH as owner/name.wiki, follows the repository's read access, and refuses pushes.
func TestWikiOverSSH(t *testing.T) {
	h := newHarness(t)
	r := h.repo("alice", "proj", true)
	wk := wiki.Wiki{Path: h.disk.WikiPath(r.ID), Git: "git", Timeout: 30 * time.Second}
	if _, err := wk.Save(context.Background(), wiki.Home, "", []byte("halo wiki"), wiki.Author{Name: "alice", Email: "a@x.invalid"}, "Home", ""); err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "wiki")
	if out, err := h.git("alice", t.TempDir(), "clone", "-q", h.url("alice/proj.wiki"), clone); err != nil {
		t.Fatalf("clone wiki: %v %s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "Home.md")); string(b) != "halo wiki" {
		t.Fatalf("Home.md: %q", b)
	}
	_ = os.WriteFile(filepath.Join(clone, "Home.md"), []byte("x"), 0o600)
	h.git("alice", clone, "commit", "-qam", "x")
	if out, err := h.git("alice", clone, "push", "-q", "origin", "main"); err == nil || !strings.Contains(out, "edited on the web") {
		t.Fatalf("push to wiki over SSH: %v %s", err, out)
	}
	if _, err := h.git("bob", t.TempDir(), "clone", "-q", h.url("alice/proj.wiki"), filepath.Join(t.TempDir(), "b")); err == nil {
		t.Fatal("bob cloned the wiki of a private repository")
	}
}
