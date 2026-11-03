package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestImportGitHubButton: an admin on a github.com mirror imports issues and comments through the API with the
// mirror's token; the issue shows its GitHub author linked to the original; non-admins cannot import (D72).
func TestImportGitHubButton(t *testing.T) {
	h := newHarness(t)
	pub, _ := h.seed(t)
	ctx := context.Background()
	tokenEnc, _ := h.s.Mirror.Key.Seal("GHTOKEN")
	if err := h.st.SetMirror(ctx, pub.ID, "https://github.com/someone/awam.git", tokenEnc, 60); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer GHTOKEN" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/someone/awam/issues":
			_, _ = w.Write([]byte(`[{"id":11,"number":7,"title":"Dari GitHub","body":"isi","state":"open","user":{"login":"orang-luar"},"html_url":"https://github.com/someone/awam/issues/7","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z","labels":[]}]`))
		case "/repos/someone/awam/issues/comments":
			_, _ = w.Write([]byte(`[{"id":21,"body":"komen luar","user":{"login":"pengulas"},"html_url":"https://github.com/someone/awam/issues/7#c","issue_url":"https://api.github.com/repos/someone/awam/issues/7","created_at":"2026-07-01T11:00:00Z"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer api.Close()
	h.s.GitHubAPI = api.URL
	b := h.browser()
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if code, _, _ := b.post("/alice/awam/import-github", url.Values{}); code != 403 {
		t.Fatalf("non-admin import: %d", code)
	}
	_, _ = h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`)
	if _, body := b.get("/alice/awam", "en"); !strings.Contains(body, "Import issues and pull requests from GitHub") {
		t.Fatal("import button not offered on a github.com mirror")
	}
	if code, _, _ := b.post("/alice/awam/import-github", url.Values{}); code != 303 {
		t.Fatalf("import: %d", code)
	}
	code, body := b.get("/alice/awam/isu/7", "en")
	if code != 200 || !strings.Contains(body, `href="https://github.com/someone/awam/issues/7" rel="noopener nofollow">orang-luar</a>`) || !strings.Contains(body, "komen luar") || !strings.Contains(body, `class="lencana lencana-gh"`) {
		t.Fatalf("imported issue page: %d", code)
	}
	if code, _, _ := b.post("/alice/awam/import-github", url.Values{}); code != 303 {
		t.Fatal("second import")
	}
	if n, _ := h.st.Comments(ctx, mustIssueID(t, h, pub.ID, 7)); len(n) != 1 {
		t.Fatalf("second import duplicated comments: %d", len(n))
	}
}

func mustIssueID(t *testing.T, h *harness, repoID, n int64) int64 {
	t.Helper()
	id, err := h.st.IssueIDByNumber(context.Background(), repoID, n)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
