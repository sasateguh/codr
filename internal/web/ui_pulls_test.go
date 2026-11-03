package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// TestUIPulls: pull requests end to end over real git — anonymous reads, write access to open,
// shared numbering with issues, compare preview, diff page, conflict detection, review rules
// (not the author, write access), protected base requiring an approval, merge with a real
// merge commit, and the stale/duplicate paths.
func TestUIPulls(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	// real history: main (2 commits), ciri (+1 clean), konflik (edits the same line as main)
	w := h.work()
	run0 := func(args ...string) {
		t.Helper()
		if out, err := h.git(w, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run0("fetch", "-q", h.url("alice/awam", "alice"), "main") // the seed already pushed main
	run0("reset", "-q", "--hard", "FETCH_HEAD")
	h.commit(w, "a.txt", "satu\n")
	h.commit(w, "b.txt", "b\n")
	run := run0
	run("checkout", "-q", "-b", "ciri")
	h.commit(w, "c.txt", "c\n")
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "konflik")
	h.commit(w, "a.txt", "dua\n")
	run("checkout", "-q", "main")
	h.commit(w, "a.txt", "tiga\n")
	run("push", "-q", h.url("alice/awam", "alice"), "main", "ciri", "konflik")

	anon := h.browser()
	if code, body := anon.get("/alice/awam/pr", "en"); code != 200 || !strings.Contains(body, "No open pull requests") {
		t.Fatalf("anonymous list: %d", code)
	}
	if code, _, loc := anon.post("/alice/awam/pr", url.Values{"title": {"x"}}); code != 303 || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("anonymous create: %d %s", code, loc)
	}
	login := func(name string) *browser {
		hash, _ := auth.Hash("kata laluan "+name, auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
		_ = h.st.SetPasswordHash(ctx, h.users[name].ID, hash)
		b := h.browser()
		b.lang = "en"
		b.get("/login", "en")
		if code, _, _ := b.post("/login", url.Values{"user": {name}, "password": {"kata laluan " + name}}); code != 303 {
			t.Fatalf("login %s", name)
		}
		return b
	}
	bob := login("bob")
	if code, _ := bob.get("/alice/awam/pr/baharu", "en"); code != 403 {
		t.Fatalf("bob (no write) new pull form: %d", code)
	}
	alice := login("alice")
	// an issue first: pull numbers continue the same sequence
	if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {"Isu satu"}}); code != 303 {
		t.Fatal("issue")
	}
	code, body := alice.get("/alice/awam/pr/baharu?head=ciri&base=main", "en")
	if code != 200 || !strings.Contains(body, "c c.txt") || !strings.Contains(body, "Open pull request") || strings.Contains(body, "conflicting") {
		t.Fatalf("compare preview: %d", code)
	}
	if code, body := alice.get("/alice/awam/pr/baharu?head=konflik&base=main", "en"); code != 200 || !strings.Contains(body, "1 conflicting files") {
		t.Fatalf("compare preview conflict: %d", code)
	}
	if code, body, _ := alice.post("/alice/awam/pr", url.Values{"title": {"x"}, "head": {"main"}, "base": {"main"}}); code != 400 || !strings.Contains(body, "two existing, different branches") {
		t.Fatalf("same branches: %d", code)
	}
	code, _, loc := alice.post("/alice/awam/pr", url.Values{"title": {"Ciri c"}, "body": {"Tutup #1."}, "head": {"ciri"}, "base": {"main"}})
	if code != 303 || loc != "/alice/awam/pr/2" {
		t.Fatalf("create (#2 after issue #1): %d %s", code, loc)
	}
	if code, _, loc := alice.post("/alice/awam/pr", url.Values{"title": {"lagi"}, "head": {"ciri"}, "base": {"main"}}); code != 303 || loc != "/alice/awam/pr/2" {
		t.Fatalf("duplicate pair goes to the open pull: %d %s", code, loc)
	}
	code, body = alice.get("/alice/awam/pr/2", "en")
	for _, want := range []string{"Pull request #2 opened", "can be merged", "/pr/2/gabung", `href="/alice/awam/isu/1"`} {
		if code != 200 || !strings.Contains(body, want) {
			t.Fatalf("pull page: %d, missing %q", code, want)
		}
	}
	if strings.Contains(body, "/pr/2/semak") {
		t.Fatal("author must not get a review form")
	}
	if code, _ := alice.get("/alice/awam/isu/2", "en"); code != 303 {
		t.Fatalf("issue #2 is a pull, must redirect: %d", code)
	}
	if code, body := alice.get("/alice/awam/pr/2/fail", "en"); code != 200 || !strings.Contains(body, "c.txt") || !strings.Contains(body, `class="baris-kod tambah"`) || strings.Contains(body, "a.txt") {
		t.Fatalf("files page (three-dot diff: main's own change excluded): %d", code)
	}
	// line comments (D65): the number links to a form under the line; the comment lands under it, in the
	// conversation, and turns "outdated" once the head moves on; a line outside the diff is refused
	if _, body := alice.get("/alice/awam/pr/2/fail?ulas_fail=c.txt&ulas_sisi=new&ulas_baris=1", "en"); !strings.Contains(body, `id="borang-ulas"`) || !strings.Contains(body, `href="?ulas_baris=1&amp;ulas_fail=c.txt&amp;ulas_sisi=new#borang-ulas"`) {
		t.Fatal("line comment form not opened")
	}
	if code, _, loc := anon.post("/alice/awam/pr/2/baris", url.Values{"path": {"c.txt"}, "side": {"new"}, "line": {"1"}, "body": {"x"}}); code != 303 || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("anonymous line comment: %d %s", code, loc)
	}
	if code, _, _ := alice.post("/alice/awam/pr/2/baris", url.Values{"path": {"a.txt"}, "side": {"new"}, "line": {"1"}, "body": {"bukan dalam diff"}}); code != 400 {
		t.Fatalf("line outside the diff: %d", code)
	}
	code, _, loc = alice.post("/alice/awam/pr/2/baris", url.Values{"path": {"c.txt"}, "side": {"new"}, "line": {"1"}, "body": {"Huruf **c** sahaja?"}})
	if code != 303 || !strings.Contains(loc, "/pr/2/fail#ulasan-") {
		t.Fatalf("line comment: %d %s", code, loc)
	}
	if _, body := alice.get("/alice/awam/pr/2/fail", "en"); !strings.Contains(body, `class="nota-baris" id="ulasan-`) || !strings.Contains(body, "<strong>c</strong>") || strings.Contains(body, "outdated") {
		t.Fatal("line comment not shown under its line")
	}
	if _, body := alice.get("/alice/awam/pr/2", "en"); !strings.Contains(body, "c.txt line 1") {
		t.Fatal("line comment missing from the conversation")
	}
	run("checkout", "-q", "ciri")
	h.commit(w, "c.txt", "c\nd\n")
	run("push", "-q", h.url("alice/awam", "alice"), "ciri")
	run("checkout", "-q", "main")
	if _, body := alice.get("/alice/awam/pr/2/fail", "en"); !strings.Contains(body, ">outdated<") {
		t.Fatal("comment not marked outdated after the head moved")
	}
	if _, body := alice.get("/alice/awam/pr/2/fail?papar=belah", "en"); !strings.Contains(body, `class="diff diff-belah"`) || !strings.Contains(body, "sisi-add") {
		t.Fatal("split view missing")
	}
	// a diff longer than ui.diff_fold_lines is folded behind one "show" line (D61)
	if err := h.st.SetSetting(context.Background(), "ui.diff_fold_lines", "1", h.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	if _, body := alice.get("/alice/awam/pr/2/fail", "en"); !strings.Contains(body, `<details class="lipat-diff">`) {
		t.Fatal("long diff not folded")
	}
	_ = h.st.SetSetting(context.Background(), "ui.diff_fold_lines", "400", h.users["alice"].ID)
	if code, _, _ := alice.post("/alice/awam/pr/2/semak", url.Values{"verdict": {"approve"}}); code != 403 {
		t.Fatalf("author review: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/pr/2/komen", url.Values{"body": {"boleh"}}); code != 303 {
		t.Fatalf("bob comments on a public pull (issue rule): %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/pr/2/semak", url.Values{"verdict": {"approve"}}); code != 403 {
		t.Fatalf("bob (no write) review: %d", code)
	}
	// assignees on a pull request (D85): bob commented, so he may carry it; only write access may assign
	if code, _, _ := bob.post("/alice/awam/pr/2/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}); code != 403 {
		t.Fatalf("bob assigns: %d", code)
	}
	if code, _, loc := alice.post("/alice/awam/pr/2/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}); code != 303 || loc != "/alice/awam/pr/2" {
		t.Fatalf("assign on a pull: %d %s", code, loc)
	}
	if _, body := alice.get("/alice/awam/pr?penerima=bob", "en"); !strings.Contains(body, "/alice/awam/pr/2") || !strings.Contains(body, "assigned to bob") {
		t.Fatal("pull list filtered by assignee")
	}
	if code, _, _ := bob.post("/alice/awam/pr/2/gabung", url.Values{}); code != 403 {
		t.Fatalf("bob merge: %d", code)
	}
	// protect main: merge now needs an approval from someone other than the author
	awam := h.byPath("alice/awam")
	if err := h.st.Protect(ctx, awam.ID, "main", false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSetting(ctx, "pull.require_review", "true", 0); err != nil { // off by default (single owner), on for a team
		t.Fatal(err)
	}
	if code, body := alice.get("/alice/awam/pr/2", "en"); code != 200 || !strings.Contains(body, "One approval from someone else is required") || strings.Contains(body, "/pr/2/gabung") {
		t.Fatalf("protected base without approval: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/pr/2/gabung", url.Values{}); code != 403 {
		t.Fatalf("merge without approval: %d", code)
	}
	_ = h.st.SetCollaborator(ctx, awam.ID, h.users["bob"].ID, store.AccessWrite)
	if code, body, _ := bob.post("/alice/awam/pr/2/semak", url.Values{"verdict": {"comment"}, "body": {""}}); code != 400 || !strings.Contains(body, "needs a note") {
		t.Fatalf("comment review without note: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/pr/2/semak", url.Values{"verdict": {"changes"}, "body": {"tukar nama"}}); code != 303 {
		t.Fatalf("bob requests changes: %d", code)
	}
	if code, body := alice.get("/alice/awam/pr/2", "en"); strings.Contains(body, "/pr/2/gabung") || !strings.Contains(body, "Request changes") {
		t.Fatalf("changes requested is not an approval: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/pr/2/semak", url.Values{"verdict": {"approve"}, "body": {"ok"}}); code != 303 {
		t.Fatalf("bob approves: %d", code)
	}
	if code, body := alice.get("/alice/awam/pr/2", "en"); code != 200 || !strings.Contains(body, "/pr/2/gabung") {
		t.Fatalf("latest verdict stands → mergeable: %d", code)
	}
	if code, _, loc := alice.post("/alice/awam/pr/2/gabung", url.Values{}); code != 303 || loc != "/alice/awam/pr/2" {
		t.Fatalf("merge: %d %s", code, loc)
	}
	code, body = alice.get("/alice/awam/pr/2", "en")
	if code != 200 || !strings.Contains(body, "merged (") || !strings.Contains(body, `class="pil-keadaan merged"`) || strings.Contains(body, "/pr/2/gabung") {
		t.Fatalf("merged page: %d", code)
	}
	// main now contains c.txt with a two-parent commit authored by alice; ciri untouched
	if code, body := alice.get("/alice/awam/tree/main", "en"); code != 200 || !strings.Contains(body, "c.txt") {
		t.Fatalf("main after merge: %d", code)
	}
	if code, body := alice.get("/alice/awam/commits/main", "en"); code != 200 || !strings.Contains(body, "Merge ciri into main (#2)") {
		t.Fatalf("merge commit in log: %d", code)
	}
	p, _ := h.st.PullByNumber(ctx, awam.ID, 2)
	if !p.Merged() || p.MergedBy != "alice" || len(p.MergeCommit) != 40 {
		t.Fatalf("stored merge: %+v", p)
	}
	if code, _, _ := alice.post("/alice/awam/pr/2/keadaan", url.Values{"keadaan": {"open"}}); code != 403 {
		t.Fatalf("reopen a merged pull: %d", code)
	}
	// conflict pull: visible as such, merge refused
	if code, _, loc := alice.post("/alice/awam/pr", url.Values{"title": {"Konflik"}, "head": {"konflik"}, "base": {"main"}}); code != 303 || loc != "/alice/awam/pr/3" {
		t.Fatalf("create conflict pull: %d %s", code, loc)
	}
	if code, body := alice.get("/alice/awam/pr/3", "en"); code != 200 || !strings.Contains(body, "There are conflicts") || !strings.Contains(body, "a.txt") || strings.Contains(body, "/pr/3/gabung") {
		t.Fatalf("conflict pull page: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/pr/3/gabung", url.Values{}); code != 403 {
		t.Fatalf("merge conflict pull: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/pr/3/keadaan", url.Values{"keadaan": {"closed"}}); code != 303 {
		t.Fatal("close")
	}
	if code, body := alice.get("/alice/awam/pr?keadaan=closed", "en"); code != 200 || !strings.Contains(body, "Konflik") || !strings.Contains(body, "Ciri c") {
		t.Fatalf("closed list holds closed + merged: %d", code)
	}
	// archived / mirror repos take no pulls; private repo invisible to bob's old self
	if code, _ := anon.get("/alice/rahsia/pr", "en"); code != 404 {
		t.Fatalf("private pulls anonymous: %d", code)
	}
	// audit trail
	rows, _, _ := h.st.AuditRows(ctx, 50, 0)
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Action] = true
	}
	for _, a := range []string{"pr.open", "pr.review", "pr.merge", "pr.close"} {
		if !seen[a] {
			t.Errorf("audit %s missing", a)
		}
	}
}
