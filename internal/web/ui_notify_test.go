package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/notify"
	"codr.my/codr/internal/store"
)

func (h *harness) login(name, pw string) *browser {
	h.t.Helper()
	hash, _ := auth.Hash(pw, auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	_ = h.st.SetPasswordHash(context.Background(), h.users[name].ID, hash)
	b := h.browser()
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {name}, "password": {pw}}); code != 303 {
		h.t.Fatalf("login %s", name)
	}
	return b
}

func (h *harness) notifs(name string) []store.Notification {
	h.t.Helper()
	ns, _, err := h.st.Notifications(context.Background(), h.users[name].ID, false, 100, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return ns
}

func TestNotifications(t *testing.T) {
	h := newHarness(t)
	pub, priv := h.seed(t)
	ctx := context.Background()
	carol, err := h.st.CreateUser(ctx, "carol", "", "x", false, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	h.users["carol"] = carol
	n := &notify.Notifier{Store: h.st, Key: h.s.Mirror.Key, Log: h.s.Log}
	tick := func() {
		t.Helper()
		if err := n.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	alice := h.login("alice", "kata laluan alice")
	bob := h.login("bob", "kata laluan bob panjang")

	// bob opens an issue on alice's public repository: alice (owner, watching by default) hears; bob does not
	bob.get("/alice/awam/isu/baharu", "en")
	if code, _, _ := bob.post("/alice/awam/isu", url.Values{"title": {"Ralat log masuk"}, "body": {"Tidak boleh log masuk."}}); code != 303 {
		t.Fatal("issue")
	}
	tick()
	if a := h.notifs("alice"); len(a) != 1 || a[0].Reason != "watching" || a[0].LastAction != "opened" || a[0].Actor != "bob" || a[0].Read {
		t.Fatalf("alice: %+v", a)
	}
	if b := h.notifs("bob"); len(b) != 0 {
		t.Fatalf("the actor was notified: %+v", b)
	}
	// the bell counts it; opening the thread reads it
	if _, body := alice.get("/", "en"); !strings.Contains(body, `class="loceng-kiraan">1<`) {
		t.Fatal("bell count")
	}
	if _, body := alice.get("/pemberitahuan", "en"); !strings.Contains(body, "Ralat log masuk") || !strings.Contains(body, "you watch this repository") {
		t.Fatal("list")
	}
	alice.get("/alice/awam/isu/1", "en")
	if a := h.notifs("alice"); !a[0].Read {
		t.Fatal("opening the issue did not read it")
	}

	// alice answers and mentions bob and carol; carol can read the public repository, so she hears too
	alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"Cuba lagi @bob, dan @carol tolong semak. Bukan `@nobody` dalam kod."}})
	tick()
	if b := h.notifs("bob"); len(b) != 1 || b[0].Reason != "mention" || b[0].LastAction != "commented" || !strings.Contains(b[0].Excerpt, "Cuba lagi") {
		t.Fatalf("bob: %+v", b)
	}
	if c := h.notifs("carol"); len(c) != 1 || c[0].Reason != "mention" {
		t.Fatalf("carol: %+v", c)
	}
	if a := h.notifs("alice"); a[0].Read == false {
		t.Fatal("alice was notified of her own comment")
	}
	// one row per thread: bob comments, alice comments again; bob still has one row, unread again
	_ = h.st.MarkRead(ctx, h.users["bob"].ID, 0)
	bob.get("/alice/awam/isu/1", "en")
	bob.post("/alice/awam/isu/1/komen", url.Values{"body": {"Sudah boleh."}})
	alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"Bagus."}})
	tick()
	if b := h.notifs("bob"); len(b) != 1 || b[0].Read || b[0].Reason != "mention" || b[0].Excerpt != "Bagus." {
		t.Fatalf("bob after more comments: %+v", b)
	}
	// edits are not news
	before := len(h.notifs("carol"))
	alice.post("/alice/awam/isu/1/sunting", url.Values{"title": {"Ralat log masuk (diperbaiki)"}, "body": {"x"}})
	tick()
	if c := h.notifs("carol"); len(c) != before || c[0].Title == "Ralat log masuk (diperbaiki)" && !c[0].Read && c[0].LastAction == "edited" {
		t.Fatalf("an edit notified: %+v", c)
	}

	// a mention in a private repository never reaches someone who cannot read it
	alice.get("/alice/rahsia/isu/baharu", "en")
	alice.post("/alice/rahsia/isu", url.Values{"title": {"Rahsia dalaman"}, "body": {"@bob @carol lihat ini"}})
	tick()
	for _, who := range []string{"bob", "carol"} {
		for _, x := range h.notifs(who) {
			if x.RepoID == priv.ID {
				t.Fatalf("%s heard about a private repository: %+v", who, x)
			}
		}
	}
	// once bob may read it, a new mention reaches him; when he loses access the row is hidden from his list
	_ = h.st.SetCollaborator(ctx, priv.ID, h.users["bob"].ID, store.AccessRead)
	alice.get("/alice/rahsia/isu/1", "en")
	alice.post("/alice/rahsia/isu/1/komen", url.Values{"body": {"@bob sekarang boleh"}})
	tick()
	var privID int64
	for _, x := range h.notifs("bob") {
		if x.RepoID == priv.ID {
			privID = x.ID
		}
	}
	if privID == 0 {
		t.Fatal("reader not notified")
	}
	_ = h.st.RemoveCollaborator(ctx, priv.ID, h.users["bob"].ID)
	if _, body := bob.get("/pemberitahuan?semua=1", "en"); strings.Contains(body, "Rahsia dalaman") || strings.Contains(body, "rahsia") {
		t.Fatal("a notification of a repository bob can no longer read is shown")
	}
	// another person's notification id answers 404
	if code, _ := alice.get("/pemberitahuan/"+strconv.FormatInt(privID, 10), "en"); code != 404 {
		t.Fatalf("someone else's notification: %d", code)
	}

	// ignore: carol ignores the repository; even a mention stays silent
	carolB := h.login("carol", "kata laluan carol panjang")
	carolB.get("/alice/awam", "en")
	if code, _, _ := carolB.post("/alice/awam/ikut", url.Values{"level": {"ignore"}}); code != 303 {
		t.Fatal("watch")
	}
	_ = h.st.MarkRead(ctx, carol.ID, 0)
	alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"@carol sekali lagi"}})
	tick()
	if c := h.notifs("carol"); c[0].Read == false {
		t.Fatalf("ignored repository notified: %+v", c)
	}
	if code, _, _ := carolB.post("/alice/awam/ikut", url.Values{"level": {"loud"}}); code != 400 {
		t.Fatal("bad level accepted")
	}
	// watching all: carol hears about a new pull request she has nothing to do with
	_ = h.st.SetWatch(ctx, carol.ID, pub.ID, store.WatchAll)
	w := h.work()
	if out, err := h.git(w, "pull", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	h.git(w, "checkout", "-q", "-b", "ciri")
	h.commit(w, "ciri.txt", "x\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "ciri"); err != nil {
		t.Fatal(out)
	}
	alice.get("/alice/awam/pr/baharu?base=main&head=ciri", "en")
	if code, _, _ := alice.post("/alice/awam/pr", url.Values{"title": {"Ciri baharu"}, "head": {"ciri"}, "base": {"main"}}); code != 303 {
		t.Fatal("pr")
	}
	tick()
	found := false
	for _, x := range h.notifs("carol") {
		found = found || (x.Thread == "pull" && x.Reason == "watching" && !x.Read)
	}
	if !found {
		t.Fatal("watcher did not hear about the pull request")
	}
	// mark all read empties the bell
	carolB.get("/pemberitahuan", "en")
	carolB.post("/pemberitahuan/baca", url.Values{"id": {"0"}})
	if c, _ := h.st.UnreadCount(ctx, carol.ID); c != 0 {
		t.Fatalf("mark all: %d", c)
	}
}

func TestMentions(t *testing.T) {
	got := notify.Mentions("Hi @Bob and @carol-2, see `@code` and\n```\n@fenced\n```\nmail a@b.com, path x/@y, @bob again")
	if strings.Join(got, ",") != "bob,carol-2" {
		t.Fatalf("%v", got)
	}
}

// TestNotifyWordsCoverEveryValue: every reason and action the notifier can write has a phrase on the list page and in
// the email, in both languages (an assignment once showed "ujipemilik · · just now" because the page had no words for it).
func TestNotifyWordsCoverEveryValue(t *testing.T) {
	for _, r := range notify.Reasons {
		if notifyWhy[r] == "" {
			t.Errorf("list page: no words for reason %q", r)
		}
		for _, lang := range []string{"ms-MY", "en-US"} {
			if k := "mail.reason." + r; i18n.T(lang, k) == k {
				t.Errorf("%s: %s missing", lang, k)
			}
		}
	}
	for _, a := range notify.Actions {
		if notifyPhrase[a] == "" {
			t.Errorf("list page: no words for action %q", a)
		}
		for _, lang := range []string{"ms-MY", "en-US"} {
			for _, th := range []string{"issue", "pull"} {
				if a == "merged" && th == "issue" || (a == "approved" || a == "changes" || a == "reviewed") && th == "issue" {
					continue // only pull requests merge or get reviews
				}
				if k := "mail.action." + a + "." + th; i18n.T(lang, k) == k {
					t.Errorf("%s: %s missing", lang, k)
				}
			}
		}
	}
}
