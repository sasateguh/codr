package web

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/xref"
)

// TestCloseFromCommits (D87): "fixes #n" in commits pushed to the default branch closes the issue (and names the
// commit), other branches close nothing, another repository closes only with write access there, a merged pull
// request closes what its body names, reopening forgets the commit, and owner/repo#n is linked only for readers.
func TestCloseFromCommits(t *testing.T) {
	h := newHarness(t)
	_, priv := h.seed(t)
	ctx := context.Background()
	c := &xref.Closer{Store: h.st, Log: h.s.Log, Git: func(id int64) gitread.Repo {
		return gitread.Repo{Path: h.disk.Path(id), Git: "git", Timeout: 30 * time.Second}
	}}
	tick := func() {
		t.Helper()
		if err := c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	alice := h.login("alice", "kata laluan alice")
	for _, title := range []string{"Satu", "Dua", "Tiga", "Empat"} {
		if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {title}, "body": {"x"}}); code != 303 {
			t.Fatal("issue")
		}
	}
	if code, _, _ := alice.post("/alice/rahsia/isu", url.Values{"title": {"Rahsia"}, "body": {"x"}}); code != 303 {
		t.Fatal("private issue")
	}
	pub := h.byPath("alice/awam")
	state := func(rp store.Repo, n int64) store.Issue {
		t.Helper()
		i, err := h.st.IssueByNumber(ctx, rp.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	w := h.work()
	run := func(args ...string) string {
		t.Helper()
		out, err := h.git(w, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}
	run("fetch", "-q", h.url("alice/awam", "alice"), "main")
	run("reset", "-q", "--hard", "FETCH_HEAD")
	// a side branch says "fixes #1": nothing closes until it reaches main
	run("checkout", "-q", "-b", "ciri")
	run("commit", "-q", "--allow-empty", "-m", "Rangka")
	run("push", "-q", h.url("alice/awam", "alice"), "ciri") // the branch exists first: the next push is an update
	run("commit", "-q", "--allow-empty", "-m", "Butang salin\n\nFixes #1. Lihat juga #2.")
	run("push", "-q", h.url("alice/awam", "alice"), "ciri")
	tick()
	if state(pub, 1).Closed() {
		t.Fatal("a commit on a side branch closed the issue")
	}
	run("checkout", "-q", "main")
	run("merge", "-q", "--ff-only", "ciri")
	run("commit", "-q", "--allow-empty", "-m", "Closes alice/rahsia#1 and closes #99")
	sha := run("rev-parse", "HEAD~1")
	run("push", "-q", h.url("alice/awam", "alice"), "main")
	tick()
	i1 := state(pub, 1)
	if !i1.Closed() || i1.ClosedBy != sha || state(pub, 2).Closed() {
		t.Fatalf("push to main: #1 %s by %q, #2 closed %v", i1.State, i1.ClosedBy, state(pub, 2).Closed())
	}
	if r := state(priv, 1); !r.Closed() || !strings.HasPrefix(r.ClosedBy, "alice/awam@") {
		t.Fatalf("cross-repository close with write access: %s %q", r.State, r.ClosedBy)
	}
	tick() // a second pass over nothing new changes nothing
	if _, body := alice.get("/alice/awam/isu/1", "en"); !strings.Contains(body, `href="/alice/awam/commit/`+sha+`">`+sha[:7]+`</a>`) {
		t.Fatal("the issue page must name the closing commit")
	}
	// bob has no write access to alice/awam: his own repository's commit cannot close her issue
	h.repo("bob", "alat", false)
	wb := h.work()
	if out, err := h.git(wb, "commit", "-q", "--allow-empty", "-m", "first"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(wb, "push", "-q", h.url("bob/alat", "bob"), "main"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(wb, "commit", "-q", "--allow-empty", "-m", "fixes alice/awam#2"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(wb, "push", "-q", h.url("bob/alat", "bob"), "main"); err != nil {
		t.Fatal(out)
	}
	tick()
	if state(pub, 2).Closed() {
		t.Fatal("someone without write access closed an issue from another repository")
	}
	// a merged pull request closes what its body names
	run("checkout", "-q", "-b", "pr")
	h.commit(w, "pr.txt", "pr\n")
	run("push", "-q", h.url("alice/awam", "alice"), "pr")
	if code, _, loc := alice.post("/alice/awam/pr", url.Values{"title": {"Tambah pr.txt"}, "body": {"Resolves #3"}, "head": {"pr"}, "base": {"main"}}); code != 303 {
		t.Fatalf("open pull: %d %s", code, loc)
	}
	tick()
	if state(pub, 3).Closed() {
		t.Fatal("an open pull request closed an issue")
	}
	if code, _, _ := alice.post("/alice/awam/pr/5/gabung", url.Values{}); code != 303 {
		t.Fatalf("merge: %d", code)
	}
	tick()
	if i3 := state(pub, 3); !i3.Closed() || i3.ClosedBy != "#5" {
		t.Fatalf("merged pull: %s %q", i3.State, i3.ClosedBy)
	}
	// a SQUASH merge keeps each commit's whole message, so "Fixes #n" in a commit body still closes the issue (D94)
	_ = h.st.SetMergeStyles(ctx, pub.ID, []string{"squash"})
	run("checkout", "-q", "-b", "padat", "pr")
	run("commit", "-q", "--allow-empty", "-m", "Kemas\n\nFixes #4.")
	run("push", "-q", h.url("alice/awam", "alice"), "padat")
	if code, _, _ := alice.post("/alice/awam/pr", url.Values{"title": {"Padat"}, "body": {"tiada kata kunci"}, "head": {"padat"}, "base": {"main"}}); code != 303 {
		t.Fatal("open squash pull")
	}
	if code, _, _ := alice.post("/alice/awam/pr/6/gabung", url.Values{"cara": {"squash"}}); code != 303 {
		t.Fatal("squash merge")
	}
	tick()
	if i4 := state(pub, 4); !i4.Closed() {
		t.Fatalf("a squash merge lost the commit body's keyword: %s", i4.State)
	}
	_ = h.st.SetMergeStyles(ctx, pub.ID, []string{"merge", "squash", "rebase"})
	// reopening forgets the closing reference; the event and audit name it
	if code, _, _ := alice.post("/alice/awam/isu/1/keadaan", url.Values{"keadaan": {"open"}}); code != 303 {
		t.Fatal("reopen")
	}
	if i := state(pub, 1); i.ClosedBy != "" {
		t.Fatalf("reopened issue keeps %q", i.ClosedBy)
	}
	// owner/repo#n: linked for a reader of that repository, plain text for anyone else
	if code, _, _ := alice.post("/alice/awam/isu/4/komen", url.Values{"body": {"Berkait alice/rahsia#1 dan alice/awam#2."}}); code != 303 {
		t.Fatal("comment")
	}
	_, body := alice.get("/alice/awam/isu/4", "en")
	if !strings.Contains(body, `href="/alice/rahsia/isu/1"`) || !strings.Contains(body, `href="/alice/awam/isu/2"`) {
		t.Fatal("the owner should see both links")
	}
	_, body = h.browser().get("/alice/awam/isu/4", "en")
	if strings.Contains(body, `href="/alice/rahsia/isu/1"`) || !strings.Contains(body, "alice/rahsia#1") || !strings.Contains(body, `href="/alice/awam/isu/2"`) {
		t.Fatal("anonymous: the private repository must stay unlinked text")
	}
	// the private issue's page names the closing commit for its reader
	if _, body := alice.get("/alice/rahsia/isu/1", "en"); !strings.Contains(body, `href="/alice/awam/commit/`) {
		t.Fatal("cross-repository closing commit not named")
	}
}
