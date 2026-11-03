package web

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
)

// TestProfilePrivacy: the profile counts only what the viewer may read. A push to a private repository
// is invisible to a visitor (no count, no row, no name) and visible to its owner (D62).
func TestProfilePrivacy(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	id := h.users["alice"].ID
	for _, target := range []string{"alice/awam", "alice/rahsia"} {
		if err := h.st.Audit(ctx, id, "git.push", target, "ssh refs/heads/main 0000000..1111111"); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.st.Audit(ctx, id, "mirror.sync", "alice/awam", "") // not an activity

	anon := h.browser()
	code, body := anon.get("/alice", "en")
	if code != 200 || !strings.Contains(body, `class="profil"`) {
		t.Fatalf("visitor profile: %d", code)
	}
	seen := count(body)
	if strings.Contains(body, "Pushed to alice/rahsia") || strings.Contains(body, "/alice/rahsia") {
		t.Fatal("a private repository leaked through the profile")
	}
	if !strings.Contains(body, "Pushed to alice/awam") || !strings.Contains(body, `class="lencana mono">main<`) {
		t.Fatal("public push missing from recent activity")
	}

	owner := h.browser()
	owner.lang = "en"
	owner.get("/login", "en")
	if code, _, _ := owner.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if _, body := owner.get("/alice", "en"); count(body) <= seen || !strings.Contains(body, "Pushed to alice/rahsia") {
		t.Fatalf("owner must see more than the visitor (%d): %s", seen, between(body, `class="profil"`, 600))
	}
	// the profile belongs to page 1 of the owner page only, never the home list
	if _, body := owner.get("/", "en"); strings.Contains(body, `class="profil"`) {
		t.Fatal("profile on the home page")
	}
}

// count reads the "N contributions in the last year" figure of a profile page.
func count(body string) int {
	m := regexp.MustCompile(`(\d+) contributions? in the last year`).FindStringSubmatch(body)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func between(s, mark string, n int) string {
	i := strings.Index(s, mark)
	if i < 0 {
		return ""
	}
	if i+n > len(s) {
		n = len(s) - i
	}
	return s[i : i+n]
}

// TestFirstSteps: the home checklist reads each step from what exists and disappears once all are done (D64).
func TestFirstSteps(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err := h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash); err != nil {
		t.Fatal(err)
	}
	b := h.browser()
	b.lang = "en"
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}}); code != 303 {
		t.Fatal("login")
	}
	// bob has a token (step 1) but no repository and no push
	if _, body := b.get("/", "en"); !strings.Contains(body, `class="mula kad"`) || strings.Count(body, `<li class="siap">`) != 1 {
		t.Fatalf("checklist: %s", between(body, `class="mula kad"`, 900))
	}
	// the dashboard feed passes the same visibility check as the profile: alice's private pushes stay hidden (D68)
	if _, body := b.get("/", "en"); !strings.Contains(body, "pushed to alice/awam") || strings.Contains(body, "alice/rahsia") {
		t.Fatal("feed shows a private repository to someone who cannot read it, or misses the public one")
	}
	h.repo("bob", "pertama", true)
	if _, body := b.get("/", "en"); strings.Count(body, `<li class="siap">`) != 2 {
		t.Fatal("repository step not ticked")
	}
	if err := h.st.Audit(ctx, h.users["bob"].ID, "git.push", "bob/pertama", "ssh refs/heads/main 0..1"); err != nil {
		t.Fatal(err)
	}
	if _, body := b.get("/", "en"); strings.Contains(body, `class="mula kad"`) {
		t.Fatal("checklist still shown after every step is done")
	}
	if _, body := h.browser().get("/", "en"); strings.Contains(body, `class="mula kad"`) {
		t.Fatal("checklist shown to a visitor")
	}
}

// TestPaletteEndpoint: the palette lists only what the viewer may read, filtered by the query (D68).
func TestPaletteEndpoint(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	anon := h.browser()
	code, body := anon.get("/api/cari?q=", "en")
	if code != 200 || !strings.Contains(body, `"p":"alice/awam"`) || strings.Contains(body, "rahsia") {
		t.Fatalf("visitor palette: %d %s", code, body)
	}
	b := h.browser()
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if _, body := b.get("/api/cari?q=rah", "en"); !strings.Contains(body, `"p":"alice/rahsia","k":true`) || strings.Contains(body, "awam") {
		t.Fatalf("owner palette with query: %s", body)
	}
}

// TestFeedThreadTitles: feed and profile lines about an issue or pull request carry its number and current title,
// read only after the visibility check, so a private thread's title never reaches someone who cannot read it.
func TestFeedThreadTitles(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	alice := h.users["alice"].ID
	for _, c := range []struct{ name, title string }{{"awam", "Public crash on login"}, {"rahsia", "Secret salary table"}} {
		repo, err := h.st.RepoByPath(ctx, "alice", c.name)
		if err != nil {
			t.Fatal(err)
		}
		is, err := h.st.CreateIssue(ctx, repo.ID, alice, c.title, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := h.st.Audit(ctx, alice, "issue.open", "alice/"+c.name+"#"+strconv.FormatInt(is.Number, 10), c.title); err != nil {
			t.Fatal(err)
		}
	}
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err := h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash); err != nil {
		t.Fatal(err)
	}
	b := h.browser()
	b.lang = "en"
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}}); code != 303 {
		t.Fatal("login")
	}
	_, body := b.get("/", "en")
	if !strings.Contains(body, `class="suapan-subjek" href="/alice/awam/isu/1"><span class="mono">#1</span> Public crash on login</a>`) {
		t.Fatalf("feed line lacks the issue title: %s", between(body, `class="suapan"`, 900))
	}
	if strings.Contains(body, "Secret salary table") {
		t.Fatal("a private issue title leaked into the feed")
	}
	_, body = h.browser().get("/alice", "en")
	if !strings.Contains(body, "Public crash on login") || strings.Contains(body, "Secret salary table") {
		t.Fatal("profile: public title missing or private title leaked")
	}
}

// TestEmptyListWayOut: an empty list never leaves a dead end. With no filters and closed items it points at them;
// with a filter it offers to drop the filter.
func TestEmptyListWayOut(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	repo, err := h.st.RepoByPath(ctx, "alice", "awam")
	if err != nil {
		t.Fatal(err)
	}
	is, err := h.st.CreateIssue(ctx, repo.ID, h.users["alice"].ID, "Done already", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetIssueState(ctx, is.ID, true); err != nil {
		t.Fatal(err)
	}
	b := h.browser()
	if _, body := b.get("/alice/awam/isu", "en"); !strings.Contains(body, `href="?keadaan=closed">See the closed ones (1)</a>`) {
		t.Fatalf("empty open list must point at the closed issue: %s", between(body, `class="kosong kad"`, 600))
	}
	_, body := b.get("/alice/awam/isu?q=nothing-like-this", "en")
	if !strings.Contains(body, `href="/alice/awam/isu?keadaan=open">Clear filters</a>`) || strings.Contains(body, "See the closed ones") {
		t.Fatalf("filtered empty list: %s", between(body, `class="kosong kad"`, 600))
	}
	if _, body := b.get("/alice/awam/pr?q=nothing-like-this", "en"); !strings.Contains(body, `href="/alice/awam/pr?keadaan=open">Clear filters</a>`) {
		t.Fatal("filtered empty pull request list has no way out")
	}
}
