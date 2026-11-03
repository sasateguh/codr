package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// TestUIIssues: issues, comments, labels and state with every permission edge — anonymous reads
// public issues but is sent to login to write; bob (no access) never sees a private repo's issues;
// bob may open an issue on a public repo only while issue.public_create is true; only the author
// or write access may close; labels need write access; "#n" in bodies links to the issue.
func TestUIIssues(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	anon := h.browser()
	if code, body := anon.get("/alice/awam/isu", "en"); code != 200 || !strings.Contains(body, "No open issues") || !strings.Contains(body, "Log in to open an issue") {
		t.Fatalf("anonymous issue list: %d", code)
	}
	if code, _ := anon.get("/alice/rahsia/isu", "en"); code != 404 {
		t.Fatalf("private issues for anonymous: %d", code)
	}
	if code, _, loc := anon.post("/alice/awam/isu", url.Values{"title": {"x"}}); code != 303 || !strings.HasPrefix(loc, "/login") {
		t.Fatalf("anonymous create: %d %s", code, loc)
	}
	alice := h.browser()
	alice.lang = "en"
	alice.get("/login", "en")
	if code, _, _ := alice.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login alice")
	}
	if code, body, _ := alice.post("/alice/awam/isu", url.Values{"title": {""}, "body": {"x"}}); code != 400 || !strings.Contains(body, "A title is required") {
		t.Fatalf("empty title: %d", code)
	}
	code, _, loc := alice.post("/alice/awam/isu", url.Values{"title": {"Butang Salin hilang"}, "body": {"Lihat #2 dan `#9`.\n\n```\n#3 dalam kod\n```\n<script>alert(1)</script>"}})
	if code != 303 || loc != "/alice/awam/isu/1" {
		t.Fatalf("create: %d %s", code, loc)
	}
	code, body := alice.get("/alice/awam/isu/1", "en")
	if code != 200 || !strings.Contains(body, "Issue #1 opened") || !strings.Contains(body, `href="/alice/awam/isu/2"`) || strings.Contains(body, "<script>") || !strings.Contains(body, "#3 dalam kod") || strings.Contains(body, `/alice/awam/isu/3"`) {
		t.Fatalf("issue page (refs linked outside code, html sanitised): %d", code)
	}
	if !strings.Contains(body, `value="closed"`) || !strings.Contains(body, "/isu/1/label") {
		t.Fatal("author with write access must see close + label forms")
	}
	// labels: create, duplicate, assign, filter, delete
	if code, _, _ := alice.post("/alice/awam/label", url.Values{"name": {"pepijat"}, "colour": {"h3"}}); code != 303 {
		t.Fatalf("create label: %d", code)
	}
	if code, body, _ := alice.post("/alice/awam/label", url.Values{"name": {"PEPIJAT"}, "colour": {"h0"}}); code != 400 || !strings.Contains(body, "already exists") {
		t.Fatalf("duplicate label: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/label", url.Values{"name": {"x"}, "colour": {"#ff0000"}}); code != 400 {
		t.Fatalf("bad colour: %d", code)
	}
	labels, _ := h.st.Labels(ctx, h.byPath("alice/awam").ID)
	lid := strconv.FormatInt(labels[0].ID, 10)
	if code, _, _ := alice.post("/alice/awam/isu/1/label", url.Values{"label": {lid}}); code != 303 {
		t.Fatalf("assign label: %d", code)
	}
	if code, body := alice.get("/alice/awam/isu?label="+lid, "en"); code != 200 || !strings.Contains(body, "Butang Salin hilang") || !strings.Contains(body, `class="label h3`) {
		t.Fatalf("filter by label: %d", code)
	}
	if code, body := alice.get("/alice/awam/isu?label=999999", "en"); code != 200 || !strings.Contains(body, "No issues match") {
		t.Fatalf("filter by unknown label: %d", code)
	}
	// comment + bad comment
	if code, body, _ := alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"  "}}); code != 400 || !strings.Contains(body, "Text is required") {
		t.Fatalf("empty comment: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"Dibaiki dalam **v0.7.1**, lihat #1."}}); code != 303 {
		t.Fatalf("comment: %d", code)
	}
	if _, body := alice.get("/alice/awam/isu/1", "en"); !strings.Contains(body, "<strong>v0.7.1</strong>") || !strings.Contains(body, "1 comment") {
		t.Fatal("comment not rendered")
	}
	if code, body := alice.get("/alice/awam", "en"); code != 200 || !strings.Contains(body, `/alice/awam/isu"`) || !strings.Contains(body, `<span class="kiraan">1</span>`) {
		t.Fatal("repo tab must show the open count")
	}
	// bob: public repo, public_create on → may open; off → 403; private → 404; cannot close alice's issue, cannot label
	bob := h.browser()
	bob.lang = "en"
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	_ = h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash)
	bob.get("/login", "en")
	if code, _, _ := bob.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}}); code != 303 {
		t.Fatal("login bob")
	}
	if code, _, loc := bob.post("/alice/awam/isu", url.Values{"title": {"Dari bob"}}); code != 303 || loc != "/alice/awam/isu/2" {
		t.Fatalf("bob opens issue on public repo: %d %s", code, loc)
	}
	if code, _, _ := bob.post("/alice/awam/isu/1/keadaan", url.Values{"keadaan": {"closed"}}); code != 403 {
		t.Fatalf("bob closes alice's issue: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/isu/2/keadaan", url.Values{"keadaan": {"closed"}}); code != 303 {
		t.Fatalf("bob closes own issue: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/isu/2/label", url.Values{"label": {lid}}); code != 403 {
		t.Fatalf("bob labels: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/label", url.Values{"name": {"y"}, "colour": {"h1"}}); code != 403 {
		t.Fatalf("bob creates label: %d", code)
	}
	if code, _ := bob.get("/alice/rahsia/isu", "en"); code != 404 {
		t.Fatalf("bob private issues: %d", code)
	}
	if code, _, _ := bob.post("/alice/rahsia/isu", url.Values{"title": {"x"}}); code != 404 {
		t.Fatalf("bob private create: %d", code)
	}
	_ = h.st.SetSetting(ctx, "issue.public_create", "false", 0)
	if code, _, _ := bob.post("/alice/awam/isu", url.Values{"title": {"lagi"}}); code != 403 {
		t.Fatalf("public_create off: %d", code)
	}
	if code, _ := bob.get("/alice/awam/isu/baharu", "en"); code != 403 {
		t.Fatalf("public_create off form: %d", code)
	}
	if code, _, _ := bob.post("/alice/awam/isu/1/komen", url.Values{"body": {"x"}}); code != 403 {
		t.Fatalf("public_create off comment: %d", code)
	}
	// bob as read collaborator on the private repo may open issues there
	_ = h.st.SetCollaborator(ctx, h.byPath("alice/rahsia").ID, h.users["bob"].ID, store.AccessRead)
	if code, _, loc := bob.post("/alice/rahsia/isu", url.Values{"title": {"laporan"}}); code != 303 || loc != "/alice/rahsia/isu/1" {
		t.Fatalf("read collaborator opens issue on private repo: %d %s", code, loc)
	}
	// alice closes, reopens, edits; closed list; archived repo refuses writes
	if code, _, _ := alice.post("/alice/awam/isu/1/keadaan", url.Values{"keadaan": {"closed"}}); code != 303 {
		t.Fatal("close")
	}
	if code, body := alice.get("/alice/awam/isu?keadaan=closed", "en"); code != 200 || !strings.Contains(body, "Butang Salin hilang") || !strings.Contains(body, "2 closed") {
		t.Fatalf("closed list: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/isu/1/keadaan", url.Values{"keadaan": {"open"}}); code != 303 {
		t.Fatal("reopen")
	}
	if code, _, _ := alice.post("/alice/awam/isu/1/sunting", url.Values{"title": {"Butang Salin hilang (disunting)"}, "body": {"baharu"}}); code != 303 {
		t.Fatal("edit")
	}
	if _, body := alice.get("/alice/awam/isu/1", "en"); !strings.Contains(body, "(disunting)") || !strings.Contains(body, "Issue edited") {
		t.Fatal("edit not applied")
	}
	if code, _ := alice.get("/alice/awam/isu/99", "en"); code != 404 {
		t.Fatalf("missing issue: %d", code)
	}
	if code, _ := alice.get("/alice/awam/isu/abc", "en"); code != 404 {
		t.Fatalf("bad number: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/label/padam", url.Values{"id": {lid}}); code != 303 {
		t.Fatal("delete label")
	}
	if _, body := alice.get("/alice/awam/isu/1", "en"); strings.Contains(body, `class="label h3`) {
		t.Fatal("label still shown after delete")
	}
	_ = h.st.SetArchived(ctx, h.byPath("alice/awam").ID, true)
	if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {"x"}}); code != 403 {
		t.Fatalf("archived create: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/isu/1/keadaan", url.Values{"keadaan": {"closed"}}); code != 403 {
		t.Fatalf("archived close: %d", code)
	}
	if code, body := alice.get("/alice/awam/isu/1", "en"); code != 200 || !strings.Contains(body, "archived") {
		t.Fatalf("archived note: %d", code)
	}
	rows, _, _ := h.st.AuditRows(ctx, 50, 0)
	var seen int
	for _, a := range rows {
		if strings.HasPrefix(a.Action, "issue.") {
			seen++
		}
	}
	if seen < 5 {
		t.Fatalf("issue actions audited: %d", seen)
	}
}

func (h *harness) byPath(path string) store.Repo {
	owner, name, _ := strings.Cut(path, "/")
	r, err := h.st.RepoByPath(context.Background(), owner, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}
