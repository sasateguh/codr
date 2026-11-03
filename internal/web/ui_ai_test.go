package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// TestAIAssistant: off by default; the admin sets an endpoint, a key (never rendered back) and opts a repository
// in; only people who may merge can ask; the request carries the diff and the asker's language; the answer is
// stored and shown labelled, to readers too (D71).
func TestAIAssistant(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.seed(t)
	ctx := context.Background()
	var mu sync.Mutex
	var asked []string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string                     `json:"model"`
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		asked = append(asked, r.Header.Get("Authorization")+"|"+body.Model+"|"+body.Messages[0].Content+"|"+body.Messages[1].Content)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Perubahan ini **menambah** fail ciri.txt."}}]}`))
	}))
	defer model.Close()
	// a feature branch and a pull request on the public repo
	w := h.work()
	if out, err := h.git(w, "fetch", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	_, _ = h.git(w, "reset", "-q", "--hard", "FETCH_HEAD")
	_, _ = h.git(w, "checkout", "-q", "-b", "ciri")
	h.commit(w, "ciri.txt", "baharu\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "ciri"); err != nil {
		t.Fatal(out)
	}
	pr, err := h.st.CreatePull(ctx, pub.ID, h.users["alice"].ID, "Tambah ciri", "Huraian PR", "ciri", "main")
	if err != nil {
		t.Fatal(err)
	}
	prPath := "/alice/awam/pr/" + strconv.FormatInt(pr.Number, 10)
	alice := h.browser()
	alice.get("/login", "en")
	if code, _, _ := alice.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if _, body := alice.get(prPath, "en"); strings.Contains(body, "Ask the assistant") {
		t.Fatal("assistant offered while off")
	}
	if code, _, _ := alice.post(prPath+"/ai", url.Values{"kind": {"summary"}}); code != 403 {
		t.Fatalf("ask while off: %d", code)
	}
	if code, _ := alice.get("/admin/ai", "en"); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	_, _ = h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`)
	form := url.Values{"endpoint": {"http://8.8.8.8/v1"}, "model": {"model-ujian"}, "key": {"KUNCI-RAHSIA-AI"}, "max_diff_kb": {"64"}, "timeout_seconds": {"20"}, "enabled": {"1"}}
	if code, body, _ := alice.post("/admin/ai", form); code != 400 || !strings.Contains(body, "private network") {
		t.Fatalf("public http endpoint accepted: %d", code)
	}
	form.Set("endpoint", model.URL+"/v1")
	form["repo"] = []string{strconv.FormatInt(pub.ID, 10)}
	if code, _, _ := alice.post("/admin/ai", form); code != 303 {
		t.Fatalf("save: %d", code)
	}
	if _, body := alice.get("/admin/ai", "en"); strings.Contains(body, "KUNCI-RAHSIA-AI") || !strings.Contains(body, "Stored key") {
		t.Fatal("key rendered back")
	}
	_, body := alice.get(prPath, "en")
	if !strings.Contains(body, "Ask the assistant") || !strings.Contains(body, "127.0.0.1") {
		t.Fatal("assistant not offered to someone who may merge")
	}
	if _, body := h.browser().get(prPath, "en"); strings.Contains(body, "Ask the assistant") {
		t.Fatal("assistant offered to a visitor")
	}
	code, _, loc := alice.post(prPath+"/ai", url.Values{"kind": {"summary"}})
	if code != 303 || !strings.Contains(loc, "#ai-") {
		t.Fatalf("ask: %d %s", code, loc)
	}
	mu.Lock()
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "Bearer KUNCI-RAHSIA-AI|model-ujian|") || !strings.Contains(asked[0], "in English") ||
		!strings.Contains(asked[0], "+++ b/ciri.txt") || !strings.Contains(asked[0], "+baharu") || !strings.Contains(asked[0], "Huraian PR") {
		t.Fatalf("what was sent: %q", asked)
	}
	mu.Unlock()
	if _, body := h.browser().get(prPath, "en"); !strings.Contains(body, "AI summary") || !strings.Contains(body, "<strong>menambah</strong>") || !strings.Contains(body, "It can be wrong") {
		t.Fatal("answer not shown to readers, or not labelled")
	}
	// opting the repository out stops it at once
	form.Del("repo")
	alice.post("/admin/ai", form)
	if code, _, _ := alice.post(prPath+"/ai", url.Values{"kind": {"review"}}); code != 403 {
		t.Fatalf("ask after opt-out: %d", code)
	}
}
