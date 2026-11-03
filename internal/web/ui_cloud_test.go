package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// bucket is a tiny in-memory S3 endpoint for the admin page test (signature present, PUT/GET/LIST/DELETE).
type bucket struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (b *bucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/baldi/")
	switch {
	case r.Method == "PUT":
		body, _ := io.ReadAll(r.Body)
		b.objs[key] = body
	case r.Method == "GET" && r.URL.Query().Get("list-type") == "2":
		_, _ = io.WriteString(w, "<ListBucketResult>")
		for k, v := range b.objs {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				_, _ = io.WriteString(w, "<Contents><Key>"+k+"</Key><Size>"+itoa(len(v))+"</Size></Contents>")
			}
		}
		_, _ = io.WriteString(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")
	case r.Method == "GET":
		_, _ = w.Write(b.objs[key])
	case r.Method == "DELETE":
		delete(b.objs, key)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestCloudAdmin: only admins reach the page; inputs are checked; the bucket secret is never rendered back;
// the recovery key appears exactly once; "Back up now" runs a real snapshot into the bucket (D70).
func TestCloudAdmin(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	if code, _ := h.browser().get("/admin/sandaran", "en"); code != 303 {
		t.Fatalf("visitor: %d", code)
	}
	admin := h.browser()
	admin.get("/login", "en")
	if code, _, _ := admin.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if code, _ := admin.get("/admin/sandaran", "en"); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	if _, err := h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if code, body := admin.get("/admin/sandaran", "en"); code != 200 || !strings.Contains(body, "Backups to your cloud") || strings.Contains(body, "no auth.secret_key") {
		t.Fatalf("admin page: %d", code)
	}
	b := &bucket{objs: map[string][]byte{}}
	srv := httptest.NewServer(b)
	defer srv.Close()
	form := url.Values{"endpoint": {"ftp://x"}, "region": {"auto"}, "bucket": {"baldi"}, "prefix": {"codr"}, "access_key": {"AK"}, "secret": {"RAHSIA-BALDI"}, "keep": {"3"}, "interval_hours": {"24"}}
	if code, body, _ := admin.post("/admin/sandaran", form); code != 400 || !strings.Contains(body, "must be an https://") {
		t.Fatalf("bad endpoint: %d", code)
	}
	form.Set("endpoint", srv.URL)
	form.Set("enabled", "1")
	if code, body, _ := admin.post("/admin/sandaran", form); code != 400 || !strings.Contains(body, "create a recovery key") {
		t.Fatalf("enabled without a key: %d", code)
	}
	form.Del("enabled")
	if code, _, _ := admin.post("/admin/sandaran", form); code != 303 {
		t.Fatalf("save: %d", code)
	}
	_, page := admin.get("/admin/sandaran", "en")
	if strings.Contains(page, "RAHSIA-BALDI") || !strings.Contains(page, "Stored. Leave empty to keep it.") {
		t.Fatal("the bucket secret was rendered back, or its stored state is not shown")
	}
	code, body, _ := admin.post("/admin/sandaran/kunci", url.Values{})
	shown := regexp.MustCompile(`(?:[0-9a-f]{8} ){7}[0-9a-f]{8}`).FindString(body)
	if code != 200 || shown == "" {
		t.Fatalf("recovery key not shown once: %d", code)
	}
	if _, again := admin.get("/admin/sandaran", "en"); strings.Contains(again, shown) {
		t.Fatal("recovery key shown a second time")
	}
	cfg, _ := h.st.CloudConfig(ctx)
	if strings.Contains(cfg.SecretSealed, "RAHSIA") || strings.Contains(cfg.KeySealed, strings.ReplaceAll(shown, " ", "")) {
		t.Fatal("secret or key stored in plain text")
	}
	if code, _, _ := admin.post("/admin/sandaran/jalan", url.Values{}); code != 303 {
		t.Fatalf("run now: %d", code)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if runs, _ := h.st.CloudRuns(ctx, 1); len(runs) == 1 && runs[0].Status != "running" {
			if runs[0].Status != "ok" {
				t.Fatalf("run: %+v", runs[0])
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	b.mu.Lock()
	n := len(b.objs)
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("bucket holds %d archives", n)
	}
	if _, body := admin.get("/admin/sandaran", "en"); !strings.Contains(body, `class="keadaan-ok"`) {
		t.Fatal("run result not on the page")
	}
}
