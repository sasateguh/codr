package runner

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testToken  = "codr_runner_test_token_0123456789"
	testSecret = "rahsia-webhook-ujian-panjang"
)

type recorded struct {
	Repo, SHA, State, Context, Description, TargetURL string
}

// fakeForge serves git over HTTP from a real bare repository (git http-backend) and records commit statuses.
type fakeForge struct {
	srv      *httptest.Server
	mu       sync.Mutex
	statuses []recorded
	authBad  int
}

func newFakeForge(t *testing.T, root string) *fakeForge {
	f := &fakeForge{}
	git, _ := exec.LookPath("git")
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/repos/{owner}/{repo}/statuses/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			f.mu.Lock()
			f.authBad++
			f.mu.Unlock()
			w.WriteHeader(401)
			return
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.mu.Lock()
		f.statuses = append(f.statuses, recorded{Repo: r.PathValue("owner") + "/" + r.PathValue("repo"), SHA: r.PathValue("sha"), State: in["state"],
			Context: in["context"], Description: in["description"], TargetURL: in["target_url"]})
		f.mu.Unlock()
		w.WriteHeader(201)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			f.mu.Lock()
			f.authBad++
			f.mu.Unlock()
			w.WriteHeader(401)
			return
		}
		backend.ServeHTTP(w, r)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeForge) final(ctx string) (recorded, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.statuses) - 1; i >= 0; i-- {
		if f.statuses[i].Context == ctx {
			return f.statuses[i], f.statuses[i].State != "pending"
		}
	}
	return recorded{}, false
}

// fakeDocker passes git through to the real binary and pretends to be docker: a script with "exit 3" fails, one with
// "sleep" runs until it is killed, anything else passes. It records every docker call.
type fakeDocker struct {
	mu    sync.Mutex
	calls [][]string
	gitAV [][]string
}

func (d *fakeDocker) Run(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) (int, error) {
	if name != "docker" {
		d.mu.Lock()
		d.gitAV = append(d.gitAV, append([]string{name}, args...))
		d.mu.Unlock()
		return osExec{}.Run(ctx, dir, env, out, name, args...)
	}
	d.mu.Lock()
	d.calls = append(d.calls, args)
	d.mu.Unlock()
	if args[0] != "run" {
		return 0, nil
	}
	script := args[len(args)-1]
	_, _ = io.WriteString(out, script)
	switch {
	case strings.Contains(script, "exit 3"):
		return 3, nil
	case strings.Contains(script, "sleep"):
		<-ctx.Done()
		return -1, ctx.Err()
	}
	return 0, nil
}

func sign(body []byte) string {
	m := hmac.New(sha256.New, []byte(testSecret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRunner (D89): signature checks, what is ignored, a push with three jobs (pass, fail, time limit) reported as
// statuses with log links, the container's lock-down flags, the token kept out of every argument, a broken ci.toml
// reported as one error status, a branch filter, and no statuses for a repository without CI.
func TestRunner(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "alice", "proj.git")
	work := filepath.Join(root, "w")
	_ = os.MkdirAll(work, 0o700)
	gitT(t, root, "init", "-q", "--bare", bare)
	gitT(t, work, "init", "-q")
	_ = os.MkdirAll(filepath.Join(work, ".codr"), 0o700)
	write := func(p, s string) { _ = os.WriteFile(filepath.Join(work, p), []byte(s), 0o600) }
	write(".codr/ci.toml", "image = \"alpine:3.20\"\nbranches = [\"main\", \"release/*\"]\n\n[jobs.lulus]\nrun = [\"echo ok\", \"true\"]\n\n[jobs.gagal]\nrun = [\"echo boom\", \"exit 3\"]\n\n[jobs.lambat]\nimage = \"busybox:1.36\"\nrun = [\"sleep 999\"]\ntimeout_minutes = 1\n")
	gitT(t, work, "add", ".")
	gitT(t, work, "commit", "-q", "-m", "ci")
	sha := gitT(t, work, "rev-parse", "HEAD")
	gitT(t, work, "push", "-q", bare, "main", "main:refs/heads/ciri")

	forge := newFakeForge(t, root)
	dk := &fakeDocker{}
	cfg := Config{ForgeURL: forge.srv.URL, PublicURL: "https://ci.example.invalid", Token: testToken, WebhookSecret: testSecret,
		WorkDir: filepath.Join(root, "kerja"), LogDir: filepath.Join(root, "log"), Lang: "en-US",
		Secrets: map[string]string{"bob/lain": "rahsia-bob-sendiri-panjang"}}
	cfg.Defaults()
	if err := cfg.Check(); err != nil {
		t.Fatal(err)
	}
	rn := &Runner{Cfg: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Exec: dk, unit: 300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rn.Start(ctx)
	h := httptest.NewServer(rn.Handler())
	defer h.Close()
	post := func(event string, body []byte, sig string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", h.URL+"/hook", bytes.NewReader(body))
		req.Header.Set("X-Codr-Event", event)
		req.Header.Set("X-Codr-Signature-256", sig)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	pushBody := func(ref, after string) []byte {
		b, _ := json.Marshal(map[string]any{"ref": ref, "after": after, "deleted": false, "repository": map[string]any{"full_name": "alice/proj"}})
		return b
	}
	body := pushBody("refs/heads/main", sha)
	if c := post("push", body, "sha256=00"); c != 401 {
		t.Fatalf("bad signature: %d", c)
	}
	if c := post("push", body, sign(append(body, ' '))); c != 401 {
		t.Fatalf("signature of another body: %d", c)
	}
	if c := post("ping", []byte(`{}`), sign([]byte(`{}`))); c != 204 {
		t.Fatalf("ping: %d", c)
	}
	// a repository with its own secret: the shared one does not sign for it
	lain, _ := json.Marshal(map[string]any{"ref": "refs/tags/v1", "after": sha, "repository": map[string]any{"full_name": "bob/lain"}})
	if c := post("push", lain, sign(lain)); c != 401 {
		t.Fatalf("shared secret for a repository with its own: %d", c)
	}
	m := hmac.New(sha256.New, []byte("rahsia-bob-sendiri-panjang"))
	m.Write(lain)
	if c := post("push", lain, "sha256="+hex.EncodeToString(m.Sum(nil))); c != 204 {
		t.Fatalf("own secret: %d", c)
	}
	tag := pushBody("refs/tags/v1", sha)
	if c := post("push", tag, sign(tag)); c != 204 {
		t.Fatalf("tag: %d", c)
	}
	if c := post("push", body, sign(body)); c != 202 {
		t.Fatalf("push: %d", c)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, a := forge.final("ci/lulus")
		_, b := forge.final("ci/gagal")
		_, c := forge.final("ci/lambat")
		if a && b && c {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("statuses: %+v", forge.statuses)
		}
		time.Sleep(50 * time.Millisecond)
	}
	lulus, _ := forge.final("ci/lulus")
	gagal, _ := forge.final("ci/gagal")
	lambat, _ := forge.final("ci/lambat")
	if lulus.State != "success" || gagal.State != "failure" || !strings.Contains(gagal.Description, "exit code 3") || lambat.State != "failure" || !strings.Contains(lambat.Description, "limit") {
		t.Fatalf("final states: %+v %+v %+v", lulus, gagal, lambat)
	}
	if lulus.SHA != sha || lulus.Repo != "alice/proj" || !strings.HasPrefix(lulus.TargetURL, "https://ci.example.invalid/ci/log/") {
		t.Fatalf("status target: %+v", lulus)
	}
	// the log behind the link
	id := strings.TrimPrefix(gagal.TargetURL, "https://ci.example.invalid/ci/log/")
	resp, err := http.Get(h.URL + "/ci/log/" + id)
	if err != nil {
		t.Fatal(err)
	}
	logText, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(logText), "$ exit 3") || !strings.Contains(string(logText), "Failed with exit code 3") || resp.Header.Get("X-Robots-Tag") == "" ||
		resp.Header.Get("Content-Security-Policy") != "default-src 'none'; sandbox" || resp.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("log page: %d %q", resp.StatusCode, logText)
	}
	for _, bad := range []string{"../../etc/passwd", "abc", strings.Repeat("g", 32)} {
		if r, _ := http.Get(h.URL + "/ci/log/" + bad); r == nil || r.StatusCode != 404 {
			t.Fatalf("log id %q answered", bad)
		}
	}
	// the containers: locked down, the checkout mounted, the right image
	dk.mu.Lock()
	var runs [][]string
	rmByName := false
	for _, c := range dk.calls {
		if c[0] == "run" {
			runs = append(runs, c)
		}
		if c[0] == "rm" && c[1] == "-f" && strings.HasPrefix(c[2], "codr-ci-") {
			rmByName = true
		}
	}
	for _, av := range dk.gitAV {
		if strings.Contains(strings.Join(av, " "), testToken) {
			t.Fatalf("the token reached an argument list: %v", av)
		}
	}
	dk.mu.Unlock()
	if len(runs) != 3 || !rmByName {
		t.Fatalf("docker runs %d, removed by name after the time limit %v", len(runs), rmByName)
	}
	for _, r := range runs {
		j := " " + strings.Join(r, " ") + " "
		for _, want := range []string{" --network none ", " --cap-drop ALL ", " --security-opt no-new-privileges ", " --read-only ", " --pids-limit 256 ", " --memory 1g ", ":/src ", " --rm "} {
			if !strings.Contains(j, want) {
				t.Fatalf("container flags miss %q: %s", want, j)
			}
		}
	}
	if !strings.Contains(strings.Join(runs[1], " "), "busybox:1.36") && !strings.Contains(strings.Join(runs[0], " "), "busybox:1.36") && !strings.Contains(strings.Join(runs[2], " "), "busybox:1.36") {
		t.Fatal("a job's own image was not used")
	}
	if forge.authBad != 0 {
		t.Fatalf("%d requests without the token", forge.authBad)
	}

	// a branch the file does not list: nothing runs
	n := len(forge.statuses)
	ciri := pushBody("refs/heads/ciri", sha)
	if c := post("push", ciri, sign(ciri)); c != 202 {
		t.Fatalf("ciri: %d", c)
	}
	// a broken file: one error status named "ci"
	write(".codr/ci.toml", "[jobs.x]\nrun = [\"true\"]\nimage = \"alpine\"\nnama_salah = 1\n")
	gitT(t, work, "commit", "-q", "-am", "rosak")
	sha2 := gitT(t, work, "rev-parse", "HEAD")
	gitT(t, work, "push", "-q", bare, "main")
	b2 := pushBody("refs/heads/main", sha2)
	post("push", b2, sign(b2))
	deadline = time.Now().Add(20 * time.Second)
	for {
		if r, done := forge.final("ci"); done {
			if r.State != "error" || r.SHA != sha2 || !strings.Contains(r.Description, "nama_salah") {
				t.Fatalf("broken file status: %+v", r)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no status for a broken file")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, s := range forge.statuses[n:] {
		if s.SHA == sha {
			t.Fatalf("a push to an unlisted branch ran jobs: %+v", s)
		}
	}
	// no CI file: nothing at all
	gitT(t, work, "rm", "-q", "-r", ".codr")
	gitT(t, work, "commit", "-q", "-m", "tiada ci")
	sha3 := gitT(t, work, "rev-parse", "HEAD")
	gitT(t, work, "push", "-q", bare, "main")
	b3 := pushBody("refs/heads/main", sha3)
	post("push", b3, sign(b3))
	time.Sleep(time.Second)
	for _, s := range forge.statuses {
		if s.SHA == sha3 {
			t.Fatalf("a commit without CI got a status: %+v", s)
		}
	}
	if es, _ := os.ReadDir(cfg.WorkDir); len(es) != 0 {
		t.Fatalf("checkouts left behind: %d", len(es))
	}
}

func TestParse(t *testing.T) {
	ok := "image = \"golang:1.23\"\n[jobs.test]\nrun = [\"go test ./...\"]\n"
	if _, jobs, err := Parse([]byte(ok), 8, 20); err != nil || len(jobs) != 1 || jobs[0].Image != "golang:1.23" {
		t.Fatalf("ok: %v %v", jobs, err)
	}
	for name, bad := range map[string]string{
		"unknown key": ok + "salah = 1\n",
		"no image":    "[jobs.test]\nrun = [\"x\"]\n",
		"bad image":   "image = \"-v /:/host\"\n[jobs.test]\nrun = [\"x\"]\n",
		"space image": "image = \"a b\"\n[jobs.test]\nrun = [\"x\"]\n",
		"bad name":    "image = \"a\"\n[jobs.\"../x\"]\nrun = [\"x\"]\n",
		"no steps":    "image = \"a\"\n[jobs.test]\nrun = []\n",
		"empty step":  "image = \"a\"\n[jobs.test]\nrun = [\" \"]\n",
		"bad glob":    "image = \"a\"\nbranches = [\"[\"]\n[jobs.test]\nrun = [\"x\"]\n",
		"not toml":    "jobs:\n  test: x\n",
	} {
		if _, _, err := Parse([]byte(bad), 8, 20); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := "image = \"a\"\n[jobs.a]\nrun = [\"x\"]\n[jobs.b]\nrun = [\"x\"]\n"
	if _, _, err := Parse([]byte(many), 1, 20); err == nil {
		t.Fatal("job limit")
	}
	if _, _, err := Parse([]byte("image = \"a\"\n"), 8, 20); err != ErrNoJobs {
		t.Fatalf("no jobs: %v", err)
	}
	j := Job{Run: []string{"echo 'a'", "make"}}
	if s := j.Script(); !strings.HasPrefix(s, "set -e\n") || !strings.Contains(s, `'$ echo '\''a'\'''`) {
		t.Fatalf("script: %q", s)
	}
	f := File{Branches: []string{"main", "release/*"}}
	if !f.Wants("main") || !f.Wants("release/1.0") || f.Wants("ciri") || !(File{}).Wants("x") {
		t.Fatal("branch filter")
	}
}

// TestGuideExamples: the .codr/ci.toml examples in docs/ci.md and docs/panduan-ci.md parse with the real parser, so the
// user guide cannot drift from the format.
func TestGuideExamples(t *testing.T) {
	for _, name := range []string{"../../docs/ci.md", "../../docs/panduan-ci.md"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		i := strings.Index(s, "```toml\n")
		j := strings.Index(s[i+8:], "```")
		if i < 0 || j < 0 {
			t.Fatalf("%s: no toml block", name)
		}
		f, jobs, err := Parse([]byte(s[i+8:i+8+j]), 8, 20)
		if err != nil || len(jobs) != 2 || !f.Wants("main") || f.Wants("ciri") {
			t.Fatalf("%s: %v %d", name, err, len(jobs))
		}
	}
}
