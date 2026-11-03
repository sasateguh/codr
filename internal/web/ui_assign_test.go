package web

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/notify"
	"codr.my/codr/internal/store"
)

// TestAssignees (D85): who may assign (write access only), who may be assigned (write access, or read access plus a
// part in the thread; never an outsider on a public repository, never anyone without read access on a private one),
// the per-thread limit, the list filter, the event and the notification that goes to the assignee alone.
func TestAssignees(t *testing.T) {
	h := newHarness(t)
	_, priv := h.seed(t)
	ctx := context.Background()
	n := &notify.Notifier{Store: h.st, Key: h.s.Mirror.Key, Log: h.s.Log}
	alice := h.login("alice", "kata laluan alice")
	bob := h.login("bob", "kata laluan bob panjang")
	if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {"Butang hilang"}, "body": {"x"}}); code != 303 {
		t.Fatal("issue")
	}
	flash := func(b *browser, form url.Values, want string) {
		t.Helper()
		code, _, loc := b.post("/alice/awam/isu/1/penerima", form)
		if code != 303 || loc != "/alice/awam/isu/1" {
			t.Fatalf("assign %v: %d %s", form, code, loc)
		}
		if _, body := b.get(loc, "en"); !strings.Contains(body, want) {
			t.Fatalf("assign %v: want %q", form, want)
		}
	}
	// bob has read access only: he may not assign anyone, not even himself
	if code, _, _ := bob.post("/alice/awam/isu/1/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}); code != 403 {
		t.Fatalf("reader assigns: %d", code)
	}
	if code, _, _ := h.browser().post("/alice/awam/isu/1/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"alice"}}); code != 403 {
		t.Fatalf("anonymous assigns: %d", code)
	}
	flash(alice, url.Values{"tindakan": {"tambah"}, "nama": {"tiada-sesiapa"}}, "There is no user named")
	// bob reads the public repository but took no part in #1: not assignable
	flash(alice, url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}, "can&#39;t be assigned here")
	if as, _ := h.st.Assignees(ctx, h.byPath("alice/awam").ID, 1); len(as) != 0 {
		t.Fatalf("outsider was assigned: %v", as)
	}
	_ = h.st.SetSetting(ctx, "issue.public_create", "true", 0)
	if code, _, _ := bob.post("/alice/awam/isu/1/komen", url.Values{"body": {"Saya boleh bantu."}}); code != 303 {
		t.Fatal("bob comments")
	}
	if err := n.Tick(ctx); err != nil { // the comment notifies alice; bob's list starts empty
		t.Fatal(err)
	}
	// carol (write collaborator) assigns bob: bob hears; alice, who watches her repository, does not
	carol, err := h.st.CreateUser(ctx, "carol", "", "x", false, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	h.users["carol"] = carol
	_ = h.st.SetCollaborator(ctx, h.byPath("alice/awam").ID, carol.ID, store.AccessWrite)
	cb := h.login("carol", "kata laluan carol panjang")
	flash(cb, url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}, "bob is now assigned")
	flash(alice, url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}, "bob is now assigned") // twice = no second row
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	ns := h.notifs("bob")
	if len(ns) != 1 || ns[0].Reason != "assigned" || ns[0].LastAction != "assigned" || ns[0].Number != 1 || ns[0].Excerpt != "" {
		t.Fatalf("bob's notification: %+v", ns)
	}
	if _, body := bob.get("/pemberitahuan", "en"); !strings.Contains(body, "assigned it to you") || !strings.Contains(body, "you&#39;re assigned") {
		t.Fatal("the list page has no words for an assignment")
	}
	if an := h.notifs("alice"); len(an) != 1 || an[0].LastAction != "commented" {
		t.Fatalf("an assignment must reach the assignee alone, alice got: %+v", an)
	}
	evs, _ := h.st.EventsAfter(ctx, 0, 100)
	assigned := 0
	for _, e := range evs {
		var d map[string]any
		_ = json.Unmarshal([]byte(e.Data), &d)
		if e.Kind == "issues" && d["action"] == "assigned" {
			assigned++
			if d["assignee"] != "bob" {
				t.Fatalf("event assignee: %v", d)
			}
		}
	}
	if assigned != 1 {
		t.Fatalf("assigned events: %d", assigned)
	}
	// the page and the list
	if _, body := bob.get("/alice/awam/isu/1", "en"); !strings.Contains(body, `href="/bob">bob</a>`) || strings.Contains(body, "/isu/1/penerima") {
		t.Fatal("reader sees the assignee but no form")
	}
	if _, body := alice.get("/alice/awam/isu?penerima=bob", "en"); !strings.Contains(body, "Butang hilang") || !strings.Contains(body, "assigned to bob") {
		t.Fatal("filter by assignee")
	}
	if _, body := alice.get("/alice/awam/isu?penerima=alice", "en"); strings.Contains(body, "Butang hilang") {
		t.Fatal("filter by someone else lists the issue")
	}
	if _, body := alice.get("/alice/awam/isu?penerima=tiada-sesiapa", "en"); strings.Contains(body, "Butang hilang") || !strings.Contains(body, "No issues match") {
		t.Fatal("filter by unknown name must list nothing")
	}
	if _, body := bob.get("/alice/awam/isu", "en"); !strings.Contains(body, "Assigned to me") || !strings.Contains(body, "penerima=bob") {
		t.Fatal("assigned-to-me link")
	}
	// the limit
	_ = h.st.SetSetting(ctx, "issue.max_assignees", "1", 0)
	flash(alice, url.Values{"tindakan": {"tambah"}, "nama": {"alice"}}, "At most 1 assignees")
	_ = h.st.SetSetting(ctx, "issue.max_assignees", "10", 0)
	flash(alice, url.Values{"tindakan": {"buang"}, "nama": {"bob"}}, "bob is no longer assigned")
	if _, body := alice.get("/alice/awam/isu?penerima=bob", "en"); strings.Contains(body, "Butang hilang") {
		t.Fatal("unassigned issue still listed")
	}
	if err := n.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if ns := h.notifs("bob"); len(ns) != 1 || ns[0].LastAction != "assigned" {
		t.Fatalf("unassigning must not notify: %+v", ns)
	}
	// private repository: bob cannot read it, so he can never carry one of its threads
	if code, _, _ := alice.post("/alice/rahsia/isu", url.Values{"title": {"Rahsia"}, "body": {"x"}}); code != 303 {
		t.Fatal("private issue")
	}
	alice.post("/alice/rahsia/isu/1/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"bob"}})
	if as, _ := h.st.Assignees(ctx, priv.ID, 1); len(as) != 0 {
		t.Fatalf("assigned on a private repository without access: %v", as)
	}
	if code, _, _ := bob.post("/alice/rahsia/isu/1/penerima", url.Values{"tindakan": {"tambah"}, "nama": {"bob"}}); code != 404 {
		t.Fatalf("outsider posting to a private repository: %d", code)
	}
}

// TestAssigneesAPI: the API applies the same rules as the page, serves pull request numbers on the issues path, shows
// assignees on issues and pulls, and filters lists by ?assignee=.
func TestAssigneesAPI(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues", "alice", map[string]any{"title": "API"}); r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "bob", map[string]any{"assignees": []string{"bob"}}); r.Code != 403 {
		t.Fatalf("reader assigns: %d", r.Code)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "", map[string]any{"assignees": []string{"alice"}}); r.Code != 401 {
		t.Fatalf("anonymous assigns: %d", r.Code)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "alice", map[string]any{"assignees": []string{"bob"}}); r.Code != 422 || !strings.Contains(string(r.Body), "not_assignable") {
		t.Fatalf("outsider: %d %s", r.Code, r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "alice", map[string]any{"assignees": []string{"tiada"}}); r.Code != 422 || !strings.Contains(string(r.Body), "unknown_user") {
		t.Fatalf("unknown: %d %s", r.Code, r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "alice", map[string]any{"assignees": []string{}}); r.Code != 422 {
		t.Fatalf("empty list: %d", r.Code)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/1/assignees", "alice", map[string]any{"assignees": []string{"alice"}}); r.Code != 201 || !strings.Contains(string(r.Body), `"login": "alice"`) {
		t.Fatalf("assign: %d %s", r.Code, r.Body)
	}
	if r := h.api("GET", "/api/v1/repos/alice/awam/issues/1", "", nil); !strings.Contains(string(r.Body), `"assignees"`) || !strings.Contains(string(r.Body), `"login": "alice"`) {
		t.Fatalf("issue shows assignees: %s", r.Body)
	}
	if l := h.api("GET", "/api/v1/repos/alice/awam/issues?assignee=alice", "", nil).list(t); len(l) != 1 {
		t.Fatalf("filter: %d", len(l))
	}
	if l := h.api("GET", "/api/v1/repos/alice/awam/issues?assignee=tiada", "", nil).list(t); len(l) != 0 {
		t.Fatalf("unknown filter must list nothing: %d", len(l))
	}
	if r := h.api("DELETE", "/api/v1/repos/alice/awam/issues/1/assignees", "alice", map[string]any{"assignees": []string{"alice"}}); r.Code != 200 || strings.Contains(string(r.Body), `"login"`) {
		t.Fatalf("unassign: %d %s", r.Code, r.Body)
	}
	if r := h.api("POST", "/api/v1/repos/alice/awam/issues/99/assignees", "alice", map[string]any{"assignees": []string{"alice"}}); r.Code != 404 {
		t.Fatalf("missing number: %d", r.Code)
	}
	if r := h.api("POST", "/api/v1/repos/alice/rahsia/issues/1/assignees", "bob", map[string]any{"assignees": []string{"bob"}}); r.Code != 404 {
		t.Fatalf("private repo for an outsider: %d", r.Code)
	}
}

// TestActivity (D86): /akaun/aktiviti shows the signed-in person's own audit rows only, filters by kind, and needs a session.
func TestActivity(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	if code, body := h.browser().get("/akaun/aktiviti", "en"); code != 303 || strings.Contains(body, "issue.open") {
		t.Fatalf("anonymous must be sent to log in: %d", code)
	}
	_ = h.st.Audit(ctx, h.users["bob"].ID, "issue.open", "alice/awam#7", "rahsia bob")
	alice := h.login("alice", "kata laluan alice")
	if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {"Satu"}, "body": {"x"}}); code != 303 {
		t.Fatal("issue")
	}
	code, body := alice.get("/akaun/aktiviti", "en")
	if code != 200 || !strings.Contains(body, "issue.open") || !strings.Contains(body, "alice/awam#1") || strings.Contains(body, "rahsia bob") {
		t.Fatalf("own rows only: %d", code)
	}
	if !strings.Contains(body, "user.login") {
		t.Fatal("the sign-in is part of the trail")
	}
	if _, body := alice.get("/akaun/aktiviti?jenis=issue", "en"); strings.Contains(body, "user.login") || !strings.Contains(body, "issue.open") {
		t.Fatal("kind filter")
	}
	if _, body := alice.get("/akaun/aktiviti?jenis=bukan-jenis", "en"); !strings.Contains(body, "user.login") {
		t.Fatal("an unknown kind is ignored, not an empty page")
	}
	if _, body := alice.get("/akaun/aktiviti?jenis=wiki", "en"); !strings.Contains(body, "No activity of this kind") {
		t.Fatal("empty kind")
	}
	if _, body := alice.get("/akaun/aktiviti", "en"); strings.Contains(body, "jenis=wiki") || !strings.Contains(body, "jenis=issue") {
		t.Fatal("only the kinds with rows are offered as filters")
	}
}
