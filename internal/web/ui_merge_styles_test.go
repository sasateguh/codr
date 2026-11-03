package web

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// TestMergeStyles (D88): the repository settings page (admins only), its validation, and merging a pull request by
// squash (one commit authored by the pull request's author) and by rebase (each commit replayed, none of them a merge),
// a style the repository turned off refused, and a rebase conflict reported without moving the branch.
func TestMergeStyles(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.seed(t)
	ctx := context.Background()
	carol, _ := h.st.CreateUser(ctx, "carol", "carol@example.invalid", "x", false, "en-US")
	h.users["carol"] = carol
	_ = h.st.SetCollaborator(ctx, pub.ID, carol.ID, store.AccessWrite)
	alice := h.login("alice", "kata laluan alice")
	bob := h.login("bob", "kata laluan bob panjang")
	cb := h.login("carol", "kata laluan carol panjang")

	// settings: 404 below admin (a writer included), validation, saving
	for _, b := range []*browser{bob, cb, h.browser()} {
		if code, _ := b.get("/alice/awam/tetapan", "en"); code != 404 {
			t.Fatalf("settings below admin: %d", code)
		}
		if code, _, _ := b.post("/alice/awam/tetapan", url.Values{"cara": {"merge"}, "utama": {"merge"}}); code != 404 {
			t.Fatalf("settings post below admin: %d", code)
		}
	}
	if code, body := alice.get("/alice/awam/tetapan", "en"); code != 200 || !strings.Contains(body, `value="squash" checked`) {
		t.Fatalf("settings page: %d", code)
	}
	if code, body, _ := alice.post("/alice/awam/tetapan", url.Values{"description": {"x"}}); code != 400 || !strings.Contains(body, "Pick at least one") {
		t.Fatalf("no style: %d", code)
	}
	if code, body, _ := alice.post("/alice/awam/tetapan", url.Values{"cara": {"merge"}, "utama": {"rebase"}}); code != 400 || !strings.Contains(body, "The default must be") {
		t.Fatalf("default not ticked: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/tetapan", url.Values{"description": {"Projek awam"}, "cara": {"rebase", "squash", "bukan"}, "utama": {"squash"}}); code != 303 {
		t.Fatalf("save: %d", code)
	}
	if r, _ := h.st.RepoByID(ctx, pub.ID); r.MergeStyles != "squash,rebase" || r.Description != "Projek awam" {
		t.Fatalf("stored: %q %q", r.MergeStyles, r.Description)
	}

	// branches: dua (two commits on main), ciri (two commits by carol), konflik (edits nota.txt like main does)
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
	run("checkout", "-q", "-b", "ciri")
	h.commit(w, "c1.txt", "1\n")
	h.commit(w, "c2.txt", "2\n")
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "konflik")
	h.commit(w, "nota.txt", "konflik\n")
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "lagi")
	h.commit(w, "l.txt", "l\n")
	run("checkout", "-q", "main")
	h.commit(w, "nota.txt", "main\n")
	run("push", "-q", h.url("alice/awam", "alice"), "main", "ciri", "konflik", "lagi")
	open := func(head string) {
		t.Helper()
		if code, _, _ := cb.post("/alice/awam/pr", url.Values{"title": {"PR " + head}, "head": {head}, "base": {"main"}}); code != 303 {
			t.Fatalf("open %s: %d", head, code)
		}
	}
	open("ciri")    // #1
	open("konflik") // #2 — merge-tree conflict: not mergeable at all
	open("lagi")    // #3
	g := gitread.Repo{Path: h.disk.Path(pub.ID), Git: "git", Timeout: 30 * time.Second}
	before, _ := g.Resolve(ctx, "main")

	if _, body := alice.get("/alice/awam/pr/1", "en"); !strings.Contains(body, `name="cara" value="squash" checked`) || strings.Contains(body, `value="merge"`) {
		t.Fatal("the merge box offers exactly the repository's styles, default first")
	}
	alice.post("/alice/awam/pr/1/gabung", url.Values{"cara": {"merge"}})
	if now, _ := g.Resolve(ctx, "main"); now != before {
		t.Fatal("a style the repository turned off was used")
	}
	if code, _, _ := alice.post("/alice/awam/pr/1/gabung", url.Values{"cara": {"squash"}}); code != 303 {
		t.Fatalf("squash: %d", code)
	}
	c, _ := g.Commit(ctx, "main")
	if len(c.Parents) != 1 || c.Parents[0] != before || c.Author != "carol" || c.Email != "carol@example.invalid" || c.Committer != "alice" || c.Subject != "PR ciri (#1)" || !strings.Contains(c.Body, "* c c1.txt") {
		t.Fatalf("squash commit: %+v", c)
	}
	if p, _ := h.st.PullByNumber(ctx, pub.ID, 1); !p.Merged() || p.MergeCommit != c.Hash {
		t.Fatalf("pull after squash: %+v", p)
	}
	// rebase: #3 replays l.txt on top of the squash commit
	before = c.Hash
	if code, _, _ := alice.post("/alice/awam/pr/3/gabung", url.Values{"cara": {"rebase"}}); code != 303 {
		t.Fatalf("rebase: %d", code)
	}
	c, _ = g.Commit(ctx, "main")
	if len(c.Parents) != 1 || c.Parents[0] != before || c.Subject != "c l.txt" || c.Committer != "alice" {
		t.Fatalf("rebased commit: %+v", c)
	}
	// a conflict only a rebase meets: the branch changes nota.txt (main changed it too) and then changes it back, so the
	// merged result is clean but replaying the first commit is not
	run("checkout", "-q", "-b", "kedua", "FETCH_HEAD")
	h.commit(w, "nota.txt", "kedua\n")
	h.commit(w, "nota.txt", "dua\n")
	run("push", "-q", h.url("alice/awam", "alice"), "kedua")
	open("kedua") // #4
	before, _ = g.Resolve(ctx, "main")
	_, _, loc := alice.post("/alice/awam/pr/4/gabung", url.Values{"cara": {"rebase"}})
	if _, body := alice.get(loc, "en"); !strings.Contains(body, "The rebase stopped on a conflict in nota.txt") {
		t.Fatal("rebase conflict not reported")
	}
	if now, _ := g.Resolve(ctx, "main"); now != before {
		t.Fatal("a conflicting rebase moved main")
	}
	if code, _ := bob.get("/alice/awam/pr/4", "en"); code != 200 {
		t.Fatal("pull page")
	}
}
