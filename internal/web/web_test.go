package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/cloud"
	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/hooks"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/mirror"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// TestMain lets git run this test binary as `amcd hook <name>` (D9): the real hook path.
func TestMain(m *testing.M) {
	hooks.RunIfHook(os.Args)
	os.Exit(m.Run())
}

type harness struct {
	t      *testing.T
	s      *Server
	st     *store.Store
	disk   repos.Disk
	srv    *httptest.Server
	users  map[string]store.User
	tokens map[string]string // user -> write token
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, st: st, disk: repos.Disk{Root: filepath.Join(dir, "repos"), GitBinary: "git"}, users: map[string]store.User{}, tokens: map[string]string{}}
	for _, name := range []string{"alice", "bob"} {
		u, err := st.CreateUser(ctx, name, "", "x", false, "en-US")
		if err != nil {
			t.Fatal(err)
		}
		h.users[name] = u
		scopes, _ := store.ParseScopes("repo:write")
		raw, _, err := st.CreateToken(ctx, u.ID, "t", scopes, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		h.tokens[name] = raw
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
	hctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = hsrv.Serve(hctx, hln) }()
	t.Cleanup(func() { cancel(); <-done })
	s := &Server{Store: st, Disk: h.disk, Git: gitcmd.Runner{Binary: "git", Timeout: 30 * time.Second, HooksDir: hooksDir, HookSocket: sock},
		MaxInputBytes: 64 << 20, DefaultLang: "en-US", Limiter: &Limiter{Max: 5, Window: time.Minute}, Log: logger,
		UI: UI{SessionTTL: time.Hour, Argon: auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1}, PasswordMinLen: 12, SSHHost: "localhost:2222", HTTPHost: "http://localhost:3000", GitTimeout: 30 * time.Second}}
	keyHex, _ := secret.Generate()
	key, _ := secret.Parse(keyHex)
	s.Mirror = &mirror.Syncer{Store: st, Disk: h.disk, Git: "git", Timeout: 30 * time.Second, Key: key, Log: logger}
	cfgFile := filepath.Join(dir, "config.toml")
	_ = os.WriteFile(cfgFile, []byte("[auth]\nsecret_key = '"+keyHex+"'\n"), 0o600)
	s.Cloud = &cloud.Job{Store: st, Disk: h.disk, GitBinary: "git", ConfigPath: cfgFile, WorkDir: filepath.Join(dir, ".cloud-tmp"), Secret: key}
	s.LFS = &lfs.Store{Root: filepath.Join(dir, "lfs")}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	h.s = s
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
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

// url builds the clone URL; cred "" = anonymous, "user" = that user's token, "user:raw" = literal.
func (h *harness) url(ref, cred string) string {
	u, _ := url.Parse(h.srv.URL + "/" + ref + ".git")
	if cred != "" {
		name, pw, ok := strings.Cut(cred, ":")
		if !ok {
			pw = h.tokens[name]
		}
		u.User = url.UserPassword(name, pw)
	}
	return u.String()
}

func (h *harness) git(dir string, args ...string) (string, error) {
	h.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-c", "http.postBuffer=524288"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "HOME="+h.t.TempDir(), "GIT_ASKPASS=")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func (h *harness) work() string {
	h.t.Helper()
	w := filepath.Join(h.t.TempDir(), "work")
	_ = os.MkdirAll(w, 0o700)
	if out, err := h.git(w, "init", "-q"); err != nil {
		h.t.Fatal(out)
	}
	return w
}

func (h *harness) commit(dir, file, content string) string {
	h.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "."}, {"commit", "-q", "-m", "c " + file}} {
		if out, err := h.git(dir, a...); err != nil {
			h.t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	out, _ := h.git(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out)
}

// get performs a raw request and returns status + body (what curl/a browser would see).
func (h *harness) get(path, auth, lang string) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestHealthzAndNoPage(t *testing.T) {
	h := newHarness(t)
	code, body := h.get("/healthz", "", "")
	if code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("healthz %d %s", code, body)
	}
	code, body = h.get("/", "", "ms")
	if code != 200 || !strings.Contains(body, "Belum ada repo") {
		t.Fatalf("root %d %s", code, body)
	}
	for _, p := range []string{"/alice/../etc/passwd.git/info/refs?service=git-upload-pack", "/alice/x.git/../../info/refs", "/Alice/x.git/info/refs?service=git-upload-pack", "/alice/x.git/HEAD", "/alice/x.git/info/refs?service=git-upload-archive"} {
		if code, _ := h.get(p, "Bearer "+h.tokens["alice"], ""); code != 404 {
			t.Fatalf("%s: %d, want 404", p, code)
		}
	}
}

func TestPublicAndPrivateAtTheHTTPDoor(t *testing.T) {
	h := newHarness(t)
	h.repo("alice", "pub", false)
	r := h.repo("alice", "secret", true)
	w := h.work()
	want := h.commit(w, "README.md", "# hi\n")
	// owner pushes with a token (Basic user:token, as git sends it)
	if out, err := h.git(w, "push", "-q", h.url("alice/pub", "alice"), "main"); err != nil {
		t.Fatalf("push: %v %s", err, out)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/secret", "alice"), "main"); err != nil {
		t.Fatalf("push secret: %v %s", err, out)
	}
	// anonymous: public clone ok, public push and private clone refused (401 — git says "Authentication failed")
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/pub", ""), "c1"); err != nil {
		t.Fatalf("anon clone pub: %v %s", err, out)
	}
	c1 := filepath.Join(t.TempDir(), "c1")
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/pub", ""), c1); err != nil {
		t.Fatal(out)
	}
	if got, _ := h.git(c1, "rev-parse", "HEAD"); strings.TrimSpace(got) != want {
		t.Fatalf("clone HEAD %s != %s", got, want)
	}
	// git answers a 401 by trying to prompt for credentials; with prompts disabled that is the failure
	if out, err := h.git(w, "push", "-q", h.url("alice/pub", ""), "main:anon"); err == nil || !strings.Contains(out, "could not read Username") {
		t.Fatalf("anon push must be refused with 401: %v %s", err, out)
	}
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/secret", ""), "c2"); err == nil || !strings.Contains(out, "could not read Username") {
		t.Fatalf("anon private clone must be refused with 401: %v %s", err, out)
	}
	// unknown and private look the same to an anonymous caller: 401, not 404
	for _, p := range []string{"/alice/secret.git/info/refs?service=git-upload-pack", "/alice/nope.git/info/refs?service=git-upload-pack", "/nobody/x.git/info/refs?service=git-upload-pack"} {
		if code, _ := h.get(p, "", ""); code != 401 {
			t.Fatalf("anon %s: %d, want 401", p, code)
		}
	}
	// bob with a valid token: private repo is not found (404), unknown repo too
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/secret", "bob"), "c3"); err == nil || !strings.Contains(out, "not found") {
		t.Fatalf("bob clone must be not-found: %v %s", err, out)
	}
	if code, body := h.get("/alice/secret.git/info/refs?service=git-upload-pack", "Bearer "+h.tokens["bob"], ""); code != 404 || !strings.Contains(body, "not found or no access: alice/secret") {
		t.Fatalf("bob raw: %d %s", code, body)
	}
	if code, _ := h.get("/alice/nope.git/info/refs?service=git-upload-pack", "Bearer "+h.tokens["bob"], ""); code != 404 {
		t.Fatal("unknown repo for bob must be 404")
	}
	// bob as reader: clone ok, push 403 with the write message
	if err := h.st.SetCollaborator(context.Background(), r.ID, h.users["bob"].ID, store.AccessRead); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/secret", "bob"), "c4"); err != nil {
		t.Fatalf("bob reader clone: %v %s", err, out)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/secret", "bob"), "main:bobs"); err == nil || !strings.Contains(out, "403") {
		t.Fatalf("bob reader push must be 403: %v %s", err, out)
	}
	if code, body := h.get("/alice/secret.git/info/refs?service=git-receive-pack", "Bearer "+h.tokens["bob"], ""); code != 403 || !strings.Contains(body, "no write access to alice/secret") {
		t.Fatalf("bob raw push: %d %s", code, body)
	}
	// archived: owner cannot push, can clone
	if err := h.st.SetArchived(context.Background(), r.ID, true); err != nil {
		t.Fatal(err)
	}
	if code, body := h.get("/alice/secret.git/info/refs?service=git-receive-pack", "Bearer "+h.tokens["alice"], ""); code != 403 || !strings.Contains(body, "archived") {
		t.Fatalf("archived: %d %s", code, body)
	}
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/secret", "alice"), "c5"); err != nil {
		t.Fatalf("archived clone: %v %s", err, out)
	}
	// audit rows carry the door
	var n int
	_ = h.st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action='git.push' AND detail LIKE 'https refs/heads/main%'`).Scan(&n)
	if n != 2 {
		t.Fatalf("https audit rows = %d", n)
	}
}

func TestTokensOnlyAndScopesAndLimiter(t *testing.T) {
	h := newHarness(t)
	h.repo("alice", "p", true)
	ctx := context.Background()
	w := h.work()
	h.commit(w, "a", "1")
	p := "/alice/p.git/info/refs?service=git-upload-pack"
	basic := func(user, pw string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pw))
	}
	if code, _ := h.get(p, basic("Alice", h.tokens["alice"]), ""); code != 200 {
		t.Fatal("user name match should be case-insensitive")
	}
	// read-only token: clone yes, push 403 naming the scope
	ro, _ := store.ParseScopes("repo:read")
	rawRO, _, _ := h.st.CreateToken(ctx, h.users["alice"].ID, "ro", ro, time.Hour)
	if out, err := h.git(w, "push", "-q", h.url("alice/p", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/p", "alice:"+rawRO), "c"); err != nil {
		t.Fatalf("ro clone: %v %s", err, out)
	}
	if code, body := h.get("/alice/p.git/info/refs?service=git-receive-pack", "Bearer "+rawRO, ""); code != 403 || !strings.Contains(body, "lacks the repo:write scope") {
		t.Fatalf("ro push: %d %s", code, body)
	}
	// a password (not a token) is refused even if it were correct; so are a wrong user name,
	// a foreign scheme, a corrupted token and an expired one — all 401, all counted
	rawOld, _, _ := h.st.CreateToken(ctx, h.users["alice"].ID, "old", ro, -time.Second)
	for _, auth := range []string{basic("alice", "x"), basic("bob", h.tokens["alice"]), "Digest abc", basic("alice", h.tokens["alice"]+"0"), "Bearer " + rawOld} {
		if code, body := h.get(p, auth, "ms"); code != 401 || !strings.Contains(body, "token sahaja") {
			t.Fatalf("%q: %d %s", auth, code, body)
		}
	}
	// limiter: after Max failures the address is blocked even with a good token
	if code, body := h.get(p, "Bearer "+h.tokens["alice"], "en"); code != 429 || !strings.Contains(body, "too many") {
		t.Fatalf("limiter: %d %s", code, body)
	}
}

func TestHooksAtTheHTTPDoor(t *testing.T) {
	h := newHarness(t)
	h.repo("alice", "g", true)
	w := h.work()
	first := h.commit(w, "a", "1")
	h.commit(w, "b", "2")
	if out, err := h.git(w, "push", "-q", h.url("alice/g", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(w, "reset", "-q", "--hard", first); err != nil {
		t.Fatal(out)
	}
	h.commit(w, "c", "3")
	if out, err := h.git(w, "push", "-q", "--force", h.url("alice/g", "alice"), "main"); err == nil || !strings.Contains(out, "branch main is protected on alice/g") {
		t.Fatalf("force push must be refused via the hook: %v %s", err, out)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/g", "alice"), ":main"); err == nil || !strings.Contains(out, "cannot be deleted") {
		t.Fatalf("delete must be refused: %v %s", err, out)
	}
	// oversize push, in Malay, streamed chunked (2 MB > http.postBuffer 512 KB set in git())
	if _, err := h.st.DB().Exec(`UPDATE users SET lang='ms-MY' WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSetting(context.Background(), "repo.push_max_mb", "1", 0); err != nil {
		t.Fatal(err)
	}
	blob := make([]byte, 2<<20)
	_, _ = rand.Read(blob)
	h.commit(w, "big.bin", string(blob))
	if out, err := h.git(w, "push", "-q", h.url("alice/g", "alice"), "main:big"); err == nil || !strings.Contains(out, "had 1 MB") {
		t.Fatalf("oversize push must be refused in Malay: %v %s", err, out)
	}
	// and a 2 MB push under a 4 MB limit goes through chunked just fine
	if err := h.st.SetSetting(context.Background(), "repo.push_max_mb", "4", 0); err != nil {
		t.Fatal(err)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/g", "alice"), "main:big"); err != nil {
		t.Fatalf("chunked push: %v %s", err, out)
	}
	c := filepath.Join(t.TempDir(), "c")
	if out, err := h.git(t.TempDir(), "clone", "-q", "-b", "big", h.url("alice/g", "alice"), c); err != nil {
		t.Fatal(out)
	}
	if st, err := os.Stat(filepath.Join(c, "big.bin")); err != nil || st.Size() != 2<<20 {
		t.Fatalf("big.bin after clone: %v", err)
	}
}

// TestClientIPTrustsHeaderOnlyFromProxy: the header names the client only when the peer is
// a trusted proxy (D44); anyone else is the peer, whatever they send.
func TestClientIPTrustsHeaderOnlyFromProxy(t *testing.T) {
	req := func(peer, hdr string) *http.Request {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = peer
		if hdr != "" {
			r.Header.Set("CF-Connecting-IP", hdr)
		}
		return r
	}
	none := &Server{}
	if got := none.clientIP(req("10.0.16.3:4000", "203.0.113.9")); got != "10.0.16.3" {
		t.Fatalf("no trust list: want the peer, got %s", got)
	}
	s := &Server{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.16.0/24")}, ClientIPHeader: "CF-Connecting-IP"}
	cases := []struct{ peer, hdr, want string }{
		{"10.0.16.3:4000", "203.0.113.9", "203.0.113.9"},          // tunnel container → header
		{"10.0.16.3:4000", " 2001:db8::7 ", "2001:db8::7"},        // v6 client, trimmed
		{"10.0.16.3:4000", "", "10.0.16.3"},                       // proxy forgot the header
		{"10.0.16.3:4000", "not-an-ip", "10.0.16.3"},              // garbage → the proxy itself
		{"198.51.100.4:4000", "203.0.113.9", "198.51.100.4"},      // untrusted peer cannot spoof
		{"[::ffff:10.0.16.3]:4000", "203.0.113.9", "203.0.113.9"}, // v4-mapped peer still trusted
	}
	for _, c := range cases {
		if got := s.clientIP(req(c.peer, c.hdr)); got != c.want {
			t.Errorf("peer %s header %q: want %s, got %s", c.peer, c.hdr, c.want, got)
		}
	}
}

// TestClientIPEdgeCheck: behind traefik (which writes X-Forwarded-For) the CDN header counts only when the
// last hop is a CDN edge; a visitor who reaches traefik directly is counted as itself, whatever header it sends.
func TestClientIPEdgeCheck(t *testing.T) {
	s := &Server{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.1.0/24")}, ClientIPHeader: "CF-Connecting-IP",
		EdgeRanges: []netip.Prefix{netip.MustParsePrefix("104.16.0.0/13")}}
	req := func(xff, hdr string) *http.Request {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "10.0.1.5:4000"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		r.Header.Set("CF-Connecting-IP", hdr)
		return r
	}
	cases := []struct{ xff, hdr, want string }{
		{"104.23.1.2", "203.0.113.9", "203.0.113.9"},              // through Cloudflare
		{"198.51.100.7", "203.0.113.9", "198.51.100.7"},           // direct to the host IP: header ignored
		{"203.0.113.1, 104.23.1.2", "203.0.113.9", "203.0.113.9"}, // only the last hop matters
		{"104.23.1.2, 198.51.100.7", "1.2.3.4", "198.51.100.7"},   // a forged edge earlier in the list does not help
		{"", "203.0.113.9", "203.0.113.9"},                        // no XFF (tunnel): D44 behaviour
		{"garbage", "203.0.113.9", "10.0.1.5"},
	}
	for _, c := range cases {
		if got := s.clientIP(req(c.xff, c.hdr)); got != c.want {
			t.Errorf("xff %q header %q: want %s, got %s", c.xff, c.hdr, c.want, got)
		}
	}
}

// TestMarkdownRelativePaths: a README's relative images and links resolve inside the repo
// (raw/ for images, blob/ for links); absolute URLs, fragments and paths above the root stay.
func TestMarkdownRelativePaths(t *testing.T) {
	src := []byte("![a](brand/logo.png) [b](docs/x.md#top) [c](https://x.y/z) [d](#here) [e](../out.md) [f](../../up.md) <img src=\"img/raw.png\">")
	h, _, err := renderMarkdown(src, &mdBase{Owner: "o", Repo: "r", Ref: "main", Dir: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`src="/o/r/raw/main/docs/brand/logo.png"`, `href="/o/r/blob/main/docs/docs/x.md#top"`,
		`href="https://x.y/z"`, `href="#here"`, `href="/o/r/blob/main/out.md"`, `href="../../up.md"`, `src="/o/r/raw/main/docs/img/raw.png"`,
	} {
		if !strings.Contains(string(h), want) {
			t.Errorf("missing %s in %s", want, h)
		}
	}
	if h2, _, _ := renderMarkdown([]byte("![a](brand/logo.png)"), nil); !strings.Contains(string(h2), `src="brand/logo.png"`) {
		t.Errorf("nil base must not rewrite: %s", h2)
	}
}
