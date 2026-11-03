package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// lfsGit runs git with the LFS filters configured on the command line (the harness gives every git call a fresh HOME,
// so `git lfs install` in a global config would not survive).
func (h *harness) lfsGit(dir string, args ...string) (string, error) {
	h.t.Helper()
	pre := []string{"-c", "filter.lfs.clean=git-lfs clean -- %f", "-c", "filter.lfs.smudge=git-lfs smudge -- %f",
		"-c", "filter.lfs.process=git-lfs filter-process", "-c", "filter.lfs.required=true", "-c", "lfs.locksverify=true"}
	return h.git(dir, append(pre, args...)...)
}

// The real git-lfs client against the server: track, push (uploads through the Batch API), clone (downloads and
// smudges), and the rules around it.
func TestLFSWithGitLFS(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed (go install github.com/git-lfs/git-lfs/v3@v3.6.1)")
	}
	h := newHarness(t)
	_, priv := h.seed(t)
	h.s.UI.HTTPHost = h.srv.URL // the Batch API answers with absolute links on http_host; here that is the test server
	big := make([]byte, 300<<10)
	_, _ = rand.Read(big)
	sum := sha256.Sum256(big)
	oid := hex.EncodeToString(sum[:])

	w := h.work()
	if out, err := h.lfsGit(w, "pull", "-q", h.url("alice/rahsia", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	_ = os.WriteFile(filepath.Join(w, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o600)
	_ = os.WriteFile(filepath.Join(w, "model.bin"), big, 0o600)
	for _, a := range [][]string{{"add", "."}, {"commit", "-q", "-m", "model"}} {
		if out, err := h.lfsGit(w, a...); err != nil {
			t.Fatalf("%v: %s", a, out)
		}
	}
	if out, _ := h.git(w, "show", "HEAD:model.bin"); !strings.Contains(out, "oid sha256:"+oid) {
		t.Fatalf("the commit holds the file, not a pointer: %.80s", out)
	}
	if out, err := h.lfsGit(w, "push", "-q", h.url("alice/rahsia", "alice"), "main"); err != nil {
		t.Fatalf("push with LFS: %s", out)
	}
	if n, ok := h.s.LFS.Stat(priv.ID, oid); !ok || n != int64(len(big)) {
		t.Fatalf("object not stored: %d %v", n, ok)
	}
	// clone elsewhere: the file comes back with its bytes
	c := filepath.Join(t.TempDir(), "klon")
	if out, err := h.lfsGit(t.TempDir(), "clone", "-q", h.url("alice/rahsia", "alice"), c); err != nil {
		t.Fatalf("clone: %s", out)
	}
	if got, _ := os.ReadFile(filepath.Join(c, "model.bin")); !bytes.Equal(got, big) {
		t.Fatalf("cloned content differs (%d bytes)", len(got))
	}
	// the web shows the pointer for what it is, and raw serves the real bytes
	b := h.login("alice", "kata laluan alice")
	if _, page := b.get("/alice/rahsia/blob/main/model.bin", "en"); !strings.Contains(page, "Stored in Git LFS, 300.0 KB") || !strings.Contains(page, oid) {
		t.Fatal("blob page")
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/alice/rahsia/raw/main/model.bin", nil)
	for _, ck := range b.c.Jar.Cookies(mustURL(h.srv.URL)) {
		req.AddCookie(ck)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(buf.Bytes(), big) {
			t.Fatal("raw did not serve the LFS object")
		}
	}

	// the rules: anonymous on a private repository is asked for credentials; bob does not see it; nobody can upload
	// bytes that do not match the oid; a read-only person cannot upload
	batch := func(path, auth, op string) (int, map[string]any) {
		body := `{"operation":"` + op + `","transfers":["basic"],"objects":[{"oid":"` + oid + `","size":` + "307200" + `}]}`
		req, _ := http.NewRequest("POST", h.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Accept", lfsMedia)
		req.Header.Set("Content-Type", lfsMedia)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+h.tokens[auth])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		if resp.StatusCode == 401 && resp.Header.Get("LFS-Authenticate") == "" {
			t.Fatal("401 without LFS-Authenticate")
		}
		return resp.StatusCode, m
	}
	if code, _ := batch("/alice/rahsia.git/info/lfs/objects/batch", "", "download"); code != 401 {
		t.Fatalf("anonymous private: %d", code)
	}
	if code, _ := batch("/alice/awam.git/info/lfs/objects/batch", "", "upload"); code != 401 {
		t.Fatalf("anonymous upload to a public repository must ask for credentials: %d", code)
	}
	if code, _ := batch("/alice/rahsia.git/info/lfs/objects/batch", "bob", "download"); code != 404 {
		t.Fatalf("bob private: %d", code)
	}
	if code, _ := batch("/alice/awam.git/info/lfs/objects/batch", "bob", "upload"); code != 403 {
		t.Fatalf("bob upload to a repository he only reads: %d", code)
	}
	if code, m := batch("/alice/rahsia.git/info/lfs/objects/batch", "alice", "download"); code != 200 || !strings.Contains(mustJSON(m), "/alice/rahsia.git/info/lfs/objects/"+oid) {
		t.Fatalf("owner download batch: %d %v", code, m)
	}
	put := func(content []byte, id string) int {
		req, _ := http.NewRequest("PUT", h.srv.URL+"/alice/awam.git/info/lfs/objects/"+id, bytes.NewReader(content))
		req.Header.Set("Authorization", "Bearer "+h.tokens["alice"])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := put([]byte("bukan kandungan itu"), oid); code != 422 {
		t.Fatalf("mismatched upload: %d", code)
	}
	if _, ok := h.s.LFS.Stat(h.byPath("alice/awam").ID, oid); ok {
		t.Fatal("a mismatched upload became an object")
	}
	if code := put([]byte("x"), "../../../../etc/passwd"); code != 422 && code != 404 {
		t.Fatalf("traversal oid: %d", code)
	}

	// SSH: git-lfs-authenticate gives a token for one repository and one direction
	out, err := h.s.LFSAuthenticate(context.Background(), h.users["alice"].ID, "alice", "rahsia.git", "download")
	var la struct {
		Href   string            `json:"href"`
		Header map[string]string `json:"header"`
	}
	if err != nil || json.Unmarshal(out, &la) != nil || !strings.HasSuffix(la.Href, "/alice/rahsia.git/info/lfs") {
		t.Fatalf("lfs-authenticate: %v %s", err, out)
	}
	sshTok := strings.TrimPrefix(la.Header["Authorization"], "Bearer ")
	try := func(path, op string) int {
		body := `{"operation":"` + op + `","objects":[{"oid":"` + oid + `","size":307200}]}`
		req, _ := http.NewRequest("POST", h.srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+sshTok)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := try("/alice/rahsia.git/info/lfs/objects/batch", "download"); c != 200 {
		t.Fatalf("ssh token download: %d", c)
	}
	if c := try("/alice/rahsia.git/info/lfs/objects/batch", "upload"); c != 403 {
		t.Fatalf("a download token uploaded: %d", c)
	}
	if c := try("/alice/awam.git/info/lfs/objects/batch", "download"); c != 401 {
		t.Fatalf("a token for one repository opened another: %d", c)
	}
	if _, err := h.s.LFSAuthenticate(context.Background(), h.users["bob"].ID, "alice", "rahsia", "download"); err == nil {
		t.Fatal("lfs-authenticate for a repository bob cannot read")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
