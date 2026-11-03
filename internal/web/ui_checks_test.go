package web

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/store"
)

// TestRequireChecks (D89): a protected-branch rule set from the repository settings page that asks for green checks
// refuses a push of a commit without statuses (or with a pending or failed one) and accepts it once every status is
// success; a pull request into that branch cannot merge until its head is green.
func TestRequireChecks(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.seed(t)
	ctx := context.Background()
	alice := h.login("alice", "kata laluan alice")
	bob := h.login("bob", "kata laluan bob panjang")
	if code, _, _ := bob.post("/alice/awam/tetapan/lindung/simpan", url.Values{"corak": {"main"}, "semakan": {"1"}}); code != 404 {
		t.Fatalf("a reader sets a rule: %d", code)
	}
	if code, body, _ := alice.post("/alice/awam/tetapan/lindung/simpan", url.Values{"corak": {"[x"}}); code != 400 || !strings.Contains(body, "isn&#39;t valid") {
		t.Fatalf("bad pattern: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/tetapan/lindung/simpan", url.Values{"corak": {"main"}, "semakan": {"1"}}); code != 303 {
		t.Fatal("save rule")
	}
	if rule, _ := h.st.RuleFor(ctx, pub, "main"); !rule.Protected || !rule.RequireChecks || rule.AllowForce {
		t.Fatalf("rule: %+v", rule)
	}
	if _, body := alice.get("/alice/awam/tetapan", "en"); !strings.Contains(body, "CI checks must pass") || !strings.Contains(body, `name="corak" value="main"`) {
		t.Fatal("the page lists the rule")
	}
	w := h.work()
	run := func(args ...string) (string, error) { return h.git(w, args...) }
	if out, err := run("fetch", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	_, _ = run("reset", "-q", "--hard", "FETCH_HEAD")
	sha := h.commit(w, "baru.txt", "baru\n")
	if out, err := run("push", "-q", h.url("alice/awam", "alice"), "HEAD:main"); err == nil || !strings.Contains(out, "only accepts a commit whose CI checks all passed") {
		t.Fatalf("push without checks: %v\n%s", err, out)
	}
	if out, err := run("push", "-q", h.url("alice/awam", "alice"), "HEAD:refs/heads/uji"); err != nil {
		t.Fatalf("an unprotected branch takes it: %s", out)
	}
	for _, st := range []struct{ state, want string }{{"pending", "still running"}, {"failure", "a check failed"}} {
		_ = h.st.SetStatus(ctx, pub.ID, h.users["alice"].ID, store.CommitStatus{SHA: sha, Context: "ci/test", State: st.state})
		if out, err := run("push", "-q", h.url("alice/awam", "alice"), "HEAD:main"); err == nil || !strings.Contains(out, st.want) {
			t.Fatalf("push with a %s check: %v\n%s", st.state, err, out)
		}
	}
	_ = h.st.SetStatus(ctx, pub.ID, h.users["alice"].ID, store.CommitStatus{SHA: sha, Context: "ci/test", State: "success"})
	if out, err := run("push", "-q", h.url("alice/awam", "alice"), "HEAD:main"); err != nil {
		t.Fatalf("a green commit must be accepted: %s", out)
	}
	// a pull request into main: blocked until its head is green
	_, _ = run("checkout", "-q", "-b", "ciri")
	head := h.commit(w, "ciri.txt", "c\n")
	if out, err := run("push", "-q", h.url("alice/awam", "alice"), "ciri"); err != nil {
		t.Fatal(out)
	}
	if code, _, _ := alice.post("/alice/awam/pr", url.Values{"title": {"Ciri"}, "head": {"ciri"}, "base": {"main"}}); code != 303 {
		t.Fatal("open pull")
	}
	if _, body := alice.get("/alice/awam/pr/1", "en"); !strings.Contains(body, "requires every CI check") || strings.Contains(body, "/pr/1/gabung") {
		t.Fatal("the merge box must say the checks are missing and offer no button")
	}
	if code, _, _ := alice.post("/alice/awam/pr/1/gabung", url.Values{}); code != 403 {
		t.Fatalf("merge without green checks: %d", code)
	}
	_ = h.st.SetStatus(ctx, pub.ID, h.users["alice"].ID, store.CommitStatus{SHA: head, Context: "ci/test", State: "success"})
	if _, body := alice.get("/alice/awam/pr/1", "en"); !strings.Contains(body, `class="senarai-semakan"`) || !strings.Contains(body, "ci/test") {
		t.Fatal("the pull request page must show the head's checks")
	}
	if code, _, _ := alice.post("/alice/awam/pr/1/gabung", url.Values{}); code != 303 {
		t.Fatalf("merge with green checks: %d", code)
	}
	if p, _ := h.st.PullByNumber(ctx, pub.ID, 1); !p.Merged() {
		t.Fatal("not merged")
	}
	if code, _, _ := alice.post("/alice/awam/tetapan/lindung/buang", url.Values{"corak": {"main"}}); code != 303 {
		t.Fatal("remove rule")
	}
	if rule, _ := h.st.RuleFor(ctx, pub, "main"); rule.RequireChecks {
		t.Fatal("rule still asks for checks")
	}
}
