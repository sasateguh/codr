package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"codr.my/codr/internal/store"
)

// TestDataReport: admins get the report, its JSON and the audit CSV; everyone gets their own data and only theirs,
// with no secret in it (D73).
func TestDataReport(t *testing.T) {
	h := newHarness(t)
	_, priv := h.seed(t)
	ctx := context.Background()
	if err := h.st.SetCollaborator(ctx, priv.ID, h.users["bob"].ID, store.AccessRead); err != nil {
		t.Fatal(err)
	}
	_ = h.st.SetSetting(ctx, "privacy.data_location", "Malaysia (Johor)", h.users["alice"].ID)
	alice := h.browser()
	alice.get("/login", "en")
	if code, _, _ := alice.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if code, _ := alice.get("/admin/data", "en"); code != 403 {
		t.Fatalf("non-admin: %d", code)
	}
	_, _ = h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`)
	code, body := alice.get("/admin/data", "en")
	if code != 200 || !strings.Contains(body, "Malaysia (Johor)") || !strings.Contains(body, `<span class="mono">bob</span> (read)`) || !strings.Contains(body, "Off: nothing is sent") {
		t.Fatalf("report page: %d", code)
	}
	get := func(path string) (*http.Response, string) {
		resp, err := alice.c.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, csvBody := get("/admin/data/audit.csv")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || !strings.HasPrefix(csvBody, "id,at,actor,action,target,detail") || !strings.Contains(csvBody, "git.push") {
		t.Fatalf("audit csv: %d %.200s", resp.StatusCode, csvBody)
	}
	resp, js := get("/admin/data/laporan.json")
	var rep dataPage
	if resp.StatusCode != 200 || json.Unmarshal([]byte(js), &rep) != nil || rep.Summary.Repos != 2 || rep.Summary.Private != 1 {
		t.Fatalf("report json: %d %.200s", resp.StatusCode, js)
	}
	// the signed-in person's own data, without hashes
	resp, mine := get("/akaun/data.json")
	var md map[string]any
	if resp.StatusCode != 200 || json.Unmarshal([]byte(mine), &md) != nil || md["account"] == nil || strings.Contains(mine, "password_hash") || strings.Contains(mine, "token_hash") {
		t.Fatalf("personal export: %d %.300s", resp.StatusCode, mine)
	}
	if code, _ := h.browser().get("/akaun/data.json", "en"); code != 303 {
		t.Fatalf("visitor export: %d", code)
	}
}
