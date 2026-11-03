package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/webhook"
)

// receiver records what a webhook endpoint got.
type receiver struct {
	mu    sync.Mutex
	got   []received
	codes []int // status to answer per request, in order; then 200
	srv   *httptest.Server
}

type received struct {
	Event, Delivery, Sig, HubSig, UA string
	Body                             []byte
}

func newReceiver(t *testing.T, codes ...int) *receiver {
	rv := &receiver{codes: codes}
	rv.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rv.mu.Lock()
		rv.got = append(rv.got, received{r.Header.Get("X-Codr-Event"), r.Header.Get("X-Codr-Delivery"), r.Header.Get("X-Codr-Signature-256"),
			r.Header.Get("X-Hub-Signature-256"), r.Header.Get("User-Agent"), b})
		code := 200
		if len(rv.codes) > 0 {
			code, rv.codes = rv.codes[0], rv.codes[1:]
		}
		rv.mu.Unlock()
		if code == 302 {
			w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte("thanks"))
	}))
	t.Cleanup(rv.srv.Close)
	return rv
}

func (rv *receiver) all() []received {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	return append([]received(nil), rv.got...)
}

func (h *harness) dispatcher() *webhook.Dispatcher {
	return &webhook.Dispatcher{Store: h.st, Key: h.s.Mirror.Key, Log: h.s.Log,
		Builder: webhook.Builder{Store: h.st, HTTPHost: h.s.UI.HTTPHost, SSHHost: h.s.UI.SSHHost, Git: h.s.git}}
}

var secretRE = regexp.MustCompile(`whsec_[0-9a-f]{48}`)

func TestWebhooks(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.seed(t)
	ctx := context.Background()
	alice := h.browser()
	alice.get("/login", "en")
	if code, _, _ := alice.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	// bob writes to the repository but does not administer it; strangers and anonymous visitors see nothing
	hash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	_ = h.st.SetPasswordHash(ctx, h.users["bob"].ID, hash)
	_ = h.st.SetCollaborator(ctx, pub.ID, h.users["bob"].ID, store.AccessWrite)
	bob := h.browser()
	bob.get("/login", "en")
	if code, _, _ := bob.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}}); code != 303 {
		t.Fatal("bob login")
	}
	for _, b := range []*browser{bob, h.browser()} {
		if code, _ := b.get("/alice/awam/webhook", "en"); code != 404 {
			t.Fatalf("webhook page below admin: %d", code)
		}
		b.get("/alice/awam", "en")
		if code, _, _ := b.post("/alice/awam/webhook", url.Values{"url": {"https://example.com/"}}); code != 404 {
			t.Fatalf("webhook create below admin: %d", code)
		}
	}
	if _, body := bob.get("/alice/awam", "en"); strings.Contains(body, "/alice/awam/webhook") {
		t.Fatal("Webhooks tab shown to a writer")
	}
	if _, body := alice.get("/alice/awam", "en"); !strings.Contains(body, `href="/alice/awam/webhook"`) {
		t.Fatal("Webhooks tab missing for the owner")
	}

	// private and local destinations are refused by default, at save time
	for _, u := range []string{"http://127.0.0.1:3000/", "http://10.0.1.1:20128/v1/chat", "http://169.254.169.254/latest/meta-data", "http://localhost:9/", "ftp://example.com/"} {
		code, body, _ := alice.post("/alice/awam/webhook", url.Values{"url": {u}})
		if code != 400 || !(strings.Contains(body, "private or local network") || strings.Contains(body, "http or https URL")) {
			t.Fatalf("%s accepted: %d", u, code)
		}
	}
	if hs, _ := h.st.Webhooks(ctx, pub.ID); len(hs) != 0 {
		t.Fatal("a refused webhook was stored")
	}

	// the receiver below lives on 127.0.0.1: allow private addresses for this test, as an admin would for a LAN CI
	_ = h.st.SetSetting(ctx, "webhook.allow_private", "true", 0)
	_ = h.st.SetSetting(ctx, "webhook.retry_seconds", "0", 0)
	rv := newReceiver(t)
	code, body, _ := alice.post("/alice/awam/webhook", url.Values{"url": {rv.srv.URL + "/hook"}, "events": {"issues", "push"}})
	sec := secretRE.FindString(body)
	if code != 200 || sec == "" {
		t.Fatalf("create: %d", code)
	}
	hs, _ := h.st.Webhooks(ctx, pub.ID)
	if len(hs) != 1 || hs[0].Events != "push,issues" || strings.Contains(hs[0].SecretSealed, sec) {
		t.Fatalf("stored: %+v", hs)
	}
	hookPath := "/alice/awam/webhook/" + strconv.FormatInt(hs[0].ID, 10)
	if _, body := alice.get(hookPath, "en"); secretRE.MatchString(body) {
		t.Fatal("secret shown a second time")
	}

	d := h.dispatcher()
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got := rv.all()
	if len(got) != 1 || got[0].Event != "ping" || !webhook.Verify(sec, got[0].Body, got[0].Sig) || got[0].HubSig != got[0].Sig ||
		!strings.HasPrefix(got[0].UA, "Codr-Hookshot/") || len(got[0].Delivery) != 32 {
		t.Fatalf("ping: %+v", got)
	}

	// an issue (subscribed) and a comment (not subscribed): one delivery, with the issue as it was when opened
	alice.get("/alice/awam/isu/baharu", "en")
	if code, _, _ := alice.post("/alice/awam/isu", url.Values{"title": {"Butang rosak"}, "body": {"Langkah: tekan."}}); code != 303 {
		t.Fatal("issue")
	}
	alice.get("/alice/awam/isu/1", "en")
	alice.post("/alice/awam/isu/1/komen", url.Values{"body": {"ok"}})
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got = rv.all()
	if len(got) != 2 || got[1].Event != "issues" || !webhook.Verify(sec, got[1].Body, got[1].Sig) {
		t.Fatalf("issue delivery: %d %+v", len(got), got)
	}
	var issue struct {
		Action string `json:"action"`
		Issue  struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			State   string `json:"state"`
			HTMLURL string `json:"html_url"`
		} `json:"issue"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Sender struct {
			Login string `json:"login"`
		} `json:"sender"`
	}
	if err := json.Unmarshal(got[1].Body, &issue); err != nil || issue.Action != "opened" || issue.Issue.Number != 1 || issue.Issue.Title != "Butang rosak" ||
		issue.Issue.State != "open" || issue.Repository.FullName != "alice/awam" || issue.Sender.Login != "alice" || !strings.HasSuffix(issue.Issue.HTMLURL, "/alice/awam/isu/1") {
		t.Fatalf("issue payload: %+v %v %s", issue, err, got[1].Body)
	}

	// a real git push over HTTPS goes through the hook server and arrives with its commits
	w := h.work()
	if out, err := h.git(w, "pull", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	before, _ := h.git(w, "rev-parse", "HEAD")
	after := h.commit(w, "baharu.txt", "isi\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	if err := d.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	got = rv.all()
	var push struct {
		Ref, Before, After string
		Commits            []struct{ ID, Message string }
		Pusher             struct{ Login string }
	}
	if len(got) != 3 || got[2].Event != "push" || json.Unmarshal(got[2].Body, &push) != nil || push.Ref != "refs/heads/main" ||
		push.Before != strings.TrimSpace(before) || push.After != after || len(push.Commits) != 1 || push.Commits[0].ID != after || push.Pusher.Login != "alice" {
		t.Fatalf("push payload: %d %s", len(got), got[len(got)-1].Body)
	}

	// failure and retry: 500 → waiting with the next try; then 200 → delivered on the second attempt
	rv.mu.Lock()
	rv.codes = []int{500, 302}
	rv.mu.Unlock()
	alice.post(hookPath+"/uji", url.Values{})
	_ = d.Tick(ctx)
	dl, _ := h.st.Deliveries(ctx, hs[0].ID, 1)
	if len(dl) != 1 || dl[0].State != "pending" || dl[0].Attempts != 1 || dl[0].StatusCode != 500 || dl[0].Response != "thanks" {
		t.Fatalf("after 500: %+v", dl)
	}
	_ = d.Tick(ctx) // 302: a redirect is not followed and counts as a failure
	dl, _ = h.st.Deliveries(ctx, hs[0].ID, 1)
	if dl[0].State != "pending" || dl[0].Attempts != 2 || dl[0].Error != "redirect not followed" {
		t.Fatalf("after 302: %+v", dl[0])
	}
	_ = d.Tick(ctx)
	dl, _ = h.st.Deliveries(ctx, hs[0].ID, 1)
	if dl[0].State != "ok" || dl[0].Attempts != 3 {
		t.Fatalf("after retry: %+v", dl[0])
	}
	if _, body := alice.get(hookPath, "en"); !strings.Contains(body, "Delivered") || !strings.Contains(body, "3 attempts") {
		t.Fatal("delivery page")
	}

	// redeliver sends the same body with a new delivery id
	n := len(rv.all())
	alice.post(hookPath+"/hantar", url.Values{"delivery": {strconv.FormatInt(dl[0].ID, 10)}})
	_ = d.Tick(ctx)
	got = rv.all()
	if len(got) != n+1 || string(got[n].Body) != string(got[n-1].Body) || got[n].Delivery == got[n-1].Delivery {
		t.Fatal("redeliver")
	}

	// switching private addresses off again stops delivery to the same saved URL at once, without retries
	_ = h.st.SetSetting(ctx, "webhook.allow_private", "false", 0)
	alice.post(hookPath+"/uji", url.Values{})
	_ = d.Tick(ctx)
	dl, _ = h.st.Deliveries(ctx, hs[0].ID, 1)
	if dl[0].State != "failed" || !strings.Contains(dl[0].Error, "private") || len(rv.all()) != n+1 {
		t.Fatalf("private after the fact: %+v", dl[0])
	}

	// a new secret is shown once and signs from then on; the old one no longer verifies
	_ = h.st.SetSetting(ctx, "webhook.allow_private", "true", 0)
	_, body, _ = alice.post(hookPath+"/rahsia", url.Values{})
	sec2 := secretRE.FindString(body)
	if sec2 == "" || sec2 == sec {
		t.Fatal("rotate")
	}
	alice.post(hookPath+"/uji", url.Values{})
	_ = d.Tick(ctx)
	got = rv.all()
	last := got[len(got)-1]
	if !webhook.Verify(sec2, last.Body, last.Sig) || webhook.Verify(sec, last.Body, last.Sig) {
		t.Fatal("rotated secret")
	}

	// another repository's path cannot reach this webhook; bob cannot act on it; alice can remove it
	h.repo("alice", "lain", false)
	if code, _, _ := alice.post("/alice/lain/webhook/"+strconv.FormatInt(hs[0].ID, 10)+"/padam", url.Values{}); code != 404 {
		t.Fatalf("cross-repo delete: %d", code)
	}
	if code, _, _ := bob.post(hookPath+"/padam", url.Values{}); code != 404 {
		t.Fatalf("writer delete: %d", code)
	}
	if code, _, _ := alice.post(hookPath+"/padam", url.Values{}); code != 303 {
		t.Fatal("delete")
	}
	if hs, _ := h.st.Webhooks(ctx, pub.ID); len(hs) != 0 {
		t.Fatal("not deleted")
	}
}
