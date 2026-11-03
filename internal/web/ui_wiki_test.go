package web

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"codr.my/codr/internal/store"
)

var baseRE = regexp.MustCompile(`name="base" value="([0-9a-f]{40})"`)

func TestWiki(t *testing.T) {
	h := newHarness(t)
	pub, priv := h.seed(t)
	ctx := context.Background()
	alice := h.login("alice", "kata laluan alice")
	bob := h.login("bob", "kata laluan bob panjang")

	// an empty wiki invites the writer; a reader only reads; anonymous on a private repository sees nothing
	if _, body := alice.get("/alice/awam/wiki", "en"); !strings.Contains(body, "Create the first page") {
		t.Fatal("empty wiki for the owner")
	}
	if _, body := bob.get("/alice/awam/wiki", "en"); strings.Contains(body, "Create the first page") || !strings.Contains(body, "No wiki pages yet") {
		t.Fatal("empty wiki for a reader")
	}
	if code, _ := h.browser().get("/alice/rahsia/wiki", "en"); code != 404 {
		t.Fatalf("private wiki anonymous: %d", code)
	}
	bob.get("/alice/awam/wiki/_baharu", "en")
	if code, _, _ := bob.post("/alice/awam/wiki/_baharu", url.Values{"title": {"Home"}, "body": {"x"}}); code != 403 {
		t.Fatalf("reader created a page: %d", code)
	}

	// create Home with a [[link]] and some HTML that must not survive
	alice.get("/alice/awam/wiki/_baharu", "en")
	code, _, loc := alice.post("/alice/awam/wiki/_baharu", url.Values{"title": {"Home"},
		"body": {"# Selamat datang\n\nLihat [[Panduan Pasang]] dan [[Soalan Lazim|soalan]].\n\n<script>alert(1)</script>\n\n```\n[[bukan pautan]]\n```\n"}})
	if code != 303 || loc != "/alice/awam/wiki/Home" {
		t.Fatalf("create: %d %s", code, loc)
	}
	_, page := bob.get("/alice/awam/wiki", "en")
	if !strings.Contains(page, `href="/alice/awam/wiki/Panduan-Pasang"`) || !strings.Contains(page, `href="/alice/awam/wiki/Soalan-Lazim" rel="nofollow">soalan<`) ||
		strings.Contains(page, "<script>alert") || !strings.Contains(page, "[[bukan pautan]]") {
		t.Fatalf("rendered Home:\n%s", page)
	}
	// a link to a missing page offers to create it (to a writer)
	if code, body := alice.get("/alice/awam/wiki/Panduan-Pasang", "en"); code != 404 || !strings.Contains(body, "Create this page") {
		t.Fatalf("missing page: %d", code)
	}

	// two editors open Home; the first saves, the second is told, not overwritten
	_, f1 := alice.get("/alice/awam/wiki/Home/sunting", "en")
	base := baseRE.FindStringSubmatch(f1)[1]
	if code, _, _ := alice.post("/alice/awam/wiki/Home/sunting", url.Values{"title": {"Home"}, "body": {"versi pertama"}, "base": {base}, "message": {"Pertama"}}); code != 303 {
		t.Fatal("first save")
	}
	if code, body, _ := alice.post("/alice/awam/wiki/Home/sunting", url.Values{"title": {"Home"}, "body": {"versi kedua"}, "base": {base}}); code != 400 || !strings.Contains(body, "Someone changed this page") || !strings.Contains(body, "versi kedua") {
		t.Fatalf("stale save: %d", code)
	}
	if _, body := alice.get("/alice/awam/wiki/Home", "en"); !strings.Contains(body, "versi pertama") {
		t.Fatal("the first save was lost")
	}

	// history and an old revision
	_, hist := alice.get("/alice/awam/wiki/Home/sejarah", "en")
	if !strings.Contains(hist, "Pertama") || !strings.Contains(hist, "Create Home") {
		t.Fatalf("history:\n%s", hist)
	}
	if _, old := alice.get("/alice/awam/wiki/Home?v="+base, "en"); !strings.Contains(old, "Selamat datang") || !strings.Contains(old, "older version") {
		t.Fatal("old revision")
	}

	// rename: a second page renamed keeps one entry; then delete it
	alice.post("/alice/awam/wiki/_baharu", url.Values{"title": {"Panduan Pasang"}, "body": {"Langkah 1"}})
	_, f2 := alice.get("/alice/awam/wiki/Panduan-Pasang/sunting", "en")
	b2 := baseRE.FindStringSubmatch(f2)[1]
	if code, _, loc := alice.post("/alice/awam/wiki/Panduan-Pasang/sunting", url.Values{"title": {"Pemasangan"}, "body": {"Langkah 1"}, "base": {b2}}); code != 303 || loc != "/alice/awam/wiki/Pemasangan" {
		t.Fatalf("rename: %d %s", code, loc)
	}
	if _, list := alice.get("/alice/awam/wiki/_semua", "en"); !strings.Contains(list, "Pemasangan") || strings.Contains(list, "Panduan Pasang<") {
		t.Fatalf("list after rename:\n%s", list)
	}
	_, f3 := alice.get("/alice/awam/wiki/Pemasangan/sunting", "en")
	if code, _, _ := alice.post("/alice/awam/wiki/Pemasangan/padam", url.Values{"base": {baseRE.FindStringSubmatch(f3)[1]}}); code != 303 {
		t.Fatal("delete")
	}
	if code, _ := alice.get("/alice/awam/wiki/Pemasangan", "en"); code != 404 {
		t.Fatal("deleted page still there")
	}
	// bob cannot delete; a bad title is refused
	if code, _, _ := bob.post("/alice/awam/wiki/Home/padam", url.Values{}); code != 403 {
		t.Fatalf("reader deleted: %d", code)
	}
	if code, _, _ := alice.post("/alice/awam/wiki/_baharu", url.Values{"title": {"../.."}, "body": {"x"}}); code != 400 {
		t.Fatalf("bad title: %d", code)
	}
	// wiki.public_edit opens a public repository's wiki to anyone signed in, never a private one's
	_ = h.st.SetSetting(ctx, "wiki.public_edit", "true", 0)
	if code, _, _ := bob.post("/alice/awam/wiki/_baharu", url.Values{"title": {"Dari Bob"}, "body": {"hai"}}); code != 303 {
		t.Fatalf("public_edit: %d", code)
	}
	_ = h.st.SetCollaborator(ctx, priv.ID, h.users["bob"].ID, store.AccessRead)
	if code, _, _ := bob.post("/alice/rahsia/wiki/_baharu", url.Values{"title": {"Home"}, "body": {"x"}}); code != 403 {
		t.Fatalf("public_edit reached a private repository: %d", code)
	}
	_ = h.st.SetSetting(ctx, "wiki.public_edit", "false", 0)

	// events for webhooks
	evs, _ := h.st.EventsAfter(ctx, 0, 500)
	wikiEvents := 0
	for _, e := range evs {
		if e.Kind == "wiki" && e.RepoID == pub.ID {
			wikiEvents++
		}
	}
	if wikiEvents < 5 {
		t.Fatalf("wiki events: %d", wikiEvents)
	}

	// git: the wiki clones over HTTPS; pushing to it is refused with a reason
	w := filepath.Join(t.TempDir(), "wiki")
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/awam.wiki", "alice"), w); err != nil {
		t.Fatalf("clone wiki: %s", out)
	}
	if b, err := os.ReadFile(filepath.Join(w, "Home.md")); err != nil || string(b) != "versi pertama" {
		t.Fatalf("cloned Home.md: %q %v", b, err)
	}
	_ = os.WriteFile(filepath.Join(w, "Home.md"), []byte("dari git"), 0o600)
	h.git(w, "commit", "-qam", "x")
	if out, err := h.git(w, "push", "-q", "origin", "main"); err == nil || !strings.Contains(out, "edited on the web") {
		t.Fatalf("push to wiki: %v %s", err, out)
	}
	// bob loses read access to the private repository: its wiki clone is refused too
	if out, err := h.git(t.TempDir(), "clone", "-q", h.url("alice/rahsia.wiki", "carol:codr_x"), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatalf("private wiki cloned with a bad token: %s", out)
	}
}
