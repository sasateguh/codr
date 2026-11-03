package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// TestForks: who may fork, what a fork carries, and that forking never publishes private code or leaks the
// name of a source the viewer lost access to (D75).
func TestForks(t *testing.T) {
	h := newHarness(t)
	_, priv := h.seed(t)
	ctx := context.Background()
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	_ = h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash)
	bob := h.browser()
	bob.lang = "en"
	bob.get("/login", "en")
	if code, _, _ := bob.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}}); code != 303 {
		t.Fatal("login")
	}
	if code, _, _ := bob.post("/alice/rahsia/fork", url.Values{}); code != 404 {
		t.Fatalf("stranger forked a private repo: %d", code)
	}
	if code, _, loc := bob.post("/alice/awam/fork", url.Values{}); code != 303 || loc != "/bob/awam" {
		t.Fatalf("fork public: %d %s", code, loc)
	}
	src, _ := h.st.RepoByPath(ctx, "alice", "awam")
	fork, err := h.st.RepoByPath(ctx, "bob", "awam")
	if err != nil || fork.ForkOf != src.ID || fork.Private {
		t.Fatalf("fork row: %+v %v", fork, err)
	}
	a, _ := h.git(h.work(), "ls-remote", h.url("alice/awam", "alice"), "refs/heads/main", "refs/tags/v1")
	b, _ := h.git(h.work(), "ls-remote", h.url("bob/awam", "bob"), "refs/heads/main", "refs/tags/v1")
	refsOnly := func(out string) string {
		var keep []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "\trefs/") {
				keep = append(keep, l)
			}
		}
		return strings.Join(keep, "\n")
	}
	a, b = refsOnly(a), refsOnly(b)
	if a == "" || a != b {
		t.Fatalf("fork refs differ:\n%s\n%s", a, b)
	}
	if _, body := bob.get("/bob/awam", "en"); !strings.Contains(body, `Forked from <a href="/alice/awam">alice/awam</a>`) {
		t.Fatal("fork line missing")
	}
	// no Fork button on your own repository: your own namespace is never a target (the copy would collide with it)
	if _, body := bob.get("/bob/awam", "en"); strings.Contains(body, `class="borang-fork"`) {
		t.Fatal("fork button offered on bob's own fork")
	}
	if code, _, _ := bob.post("/bob/awam/fork", url.Values{"owner": {"bob"}}); code != 403 {
		t.Fatalf("fork into the repository's own namespace: %d", code)
	}
	if _, body := h.browser().get("/alice/awam", "en"); !strings.Contains(body, "1 fork") {
		t.Fatal("fork count missing on the source")
	}
	if code, _, loc := bob.post("/alice/awam/fork", url.Values{}); code != 303 || loc != "/bob/awam" {
		t.Fatalf("second fork must lead to the existing one: %d %s", code, loc)
	}
	// a private source gives a private fork, whatever the form says
	_ = h.st.SetCollaborator(ctx, priv.ID, h.users["bob"].ID, store.AccessRead)
	if code, _, loc := bob.post("/alice/rahsia/fork", url.Values{"visibility": {"public"}}); code != 303 {
		t.Fatalf("reader fork: %d", code)
	} else {
		bob.get(loc, "en") // the browser follows the redirect and reads the flash while bob still has access
	}
	pf, _ := h.st.RepoByPath(ctx, "bob", "rahsia")
	if !pf.Private {
		t.Fatal("a private repository was forked into a public one")
	}
	// bob loses access: his fork stays his, but the source is not named any more
	_ = h.st.RemoveCollaborator(ctx, priv.ID, h.users["bob"].ID)
	if _, body := bob.get("/bob/rahsia", "en"); strings.Contains(body, "alice/rahsia") || !strings.Contains(body, "Forked from a repository you cannot see") {
		t.Fatal("source name leaked after access was removed")
	}
	// only into organisations bob owns
	org, _ := h.st.CreateOrg(ctx, "syarikat", h.users["alice"].ID)
	_ = h.st.SetOrgMember(ctx, org.ID, h.users["bob"].ID, "member")
	if code, _, _ := bob.post("/alice/awam/fork", url.Values{"owner": {"syarikat"}}); code != 403 {
		t.Fatalf("fork into an org bob does not own: %d", code)
	}
}

// TestPullFromFork: bob, who cannot write to alice/awam, contributes through his fork; the preview leaves nothing
// behind; the pull request follows new pushes to the fork; refs/codr/ is invisible and unwritable over git; only
// alice can merge, and the merge lands bob's commit (D75).
func TestPullFromFork(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	_ = h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash)
	login := func(name, pw string) *browser {
		b := h.browser()
		b.lang = "en"
		b.get("/login", "en")
		if code, _, _ := b.post("/login", url.Values{"user": {name}, "password": {pw}}); code != 303 {
			t.Fatalf("login %s", name)
		}
		return b
	}
	bob, alice := login("bob", "kata laluan bob"), login("alice", "kata laluan alice")
	if code, _, _ := bob.post("/alice/awam/fork", url.Values{}); code != 303 {
		t.Fatal("fork")
	}
	w := h.work()
	if out, err := h.git(w, "fetch", "-q", h.url("bob/awam", "bob"), "main"); err != nil {
		t.Fatal(out)
	}
	_, _ = h.git(w, "checkout", "-q", "-b", "cadangan", "FETCH_HEAD")
	h.commit(w, "cadangan.txt", "idea\n")
	if out, err := h.git(w, "push", "-q", h.url("bob/awam", "bob"), "cadangan"); err != nil {
		t.Fatalf("push to fork: %s", out)
	}
	if _, body := bob.get("/bob/awam", "en"); !strings.Contains(body, "Pull request to alice/awam") {
		t.Fatal("contribute button missing on the fork")
	}
	code, body := bob.get("/bob/awam/pr/hulu?head=cadangan&base=main", "en")
	if code != 200 || !strings.Contains(body, "1 commit") {
		t.Fatalf("upstream preview: %d", code)
	}
	src, _ := h.st.RepoByPath(ctx, "alice", "awam")
	if refs, _ := h.git(h.work(), "--git-dir", h.disk.Path(src.ID), "for-each-ref", "refs/codr/"); strings.TrimSpace(refs) != "" {
		t.Fatalf("the preview left a ref in the source: %q", refs)
	}
	code, _, loc := bob.post("/bob/awam/pr/hulu", url.Values{"head": {"cadangan"}, "base": {"main"}, "title": {"Satu idea"}})
	if code != 303 || !strings.HasPrefix(loc, "/alice/awam/pr/") {
		t.Fatalf("open upstream PR: %d %s", code, loc)
	}
	pr := loc
	if _, body := alice.get(pr+"/fail", "en"); !strings.Contains(body, "cadangan.txt") {
		t.Fatal("diff of the fork branch missing")
	}
	if _, body := alice.get(pr, "en"); !strings.Contains(body, "bob/awam:cadangan") {
		t.Fatal("head label missing")
	}
	// a new push to the fork shows up in the pull request
	h.commit(w, "kedua.txt", "lagi\n")
	_, _ = h.git(w, "push", "-q", h.url("bob/awam", "bob"), "cadangan")
	if _, body := alice.get(pr+"/fail", "en"); !strings.Contains(body, "kedua.txt") {
		t.Fatal("pull request did not follow the fork")
	}
	// refs/codr/ is hidden from fetch and refused on push
	if out, _ := h.git(h.work(), "ls-remote", h.url("alice/awam", "alice")); strings.Contains(out, "refs/codr/") {
		t.Fatalf("refs/codr advertised: %s", out)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "HEAD:refs/codr/pull/999"); err == nil {
		t.Fatalf("push to refs/codr accepted: %s", out)
	}
	// bob cannot merge into alice's repo; alice can
	if code, _, _ := bob.post(pr+"/gabung", url.Values{}); code != 403 {
		t.Fatalf("contributor merged: %d", code)
	}
	if code, _, _ := alice.post(pr+"/gabung", url.Values{}); code != 303 {
		t.Fatalf("owner merge: %d", code)
	}
	if _, body := alice.get(pr, "en"); !strings.Contains(body, ">bob/awam:cadangan</a>") {
		t.Fatal("a merged pull request lost its head label")
	}
	if out, _ := h.git(h.work(), "--git-dir", h.disk.Path(src.ID), "log", "--format=%s", "-3", "main"); !strings.Contains(out, "Merge bob/awam:cadangan into main") {
		t.Fatalf("merge commit: %s", out)
	}
	if out, err := h.git(h.work(), "--git-dir", h.disk.Path(src.ID), "cat-file", "-e", "main:kedua.txt"); err != nil {
		t.Fatalf("bob's file not on main: %s", out)
	}
}
