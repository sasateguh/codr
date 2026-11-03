package ghimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codr.my/codr/internal/store"
)

const issuesJSONL = `{"id":901,"number":1,"title":"Pepijat tarikh","body":"Tarikh **salah**","state":"closed","user":{"login":"ali"},"html_url":"https://github.com/o/r/issues/1","created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-02T10:00:00Z","closed_at":"2026-07-02T10:00:00Z","labels":[{"name":"bug"}]}
{"id":902,"number":2,"title":"PR dalam senarai isu","state":"open","user":{"login":"bot"},"pull_request":{"url":"x"},"created_at":"2026-07-03T10:00:00Z","updated_at":"2026-07-03T10:00:00Z"}
{"id":903,"number":3,"title":"Bertembung","body":null,"state":"open","user":{"login":"ali"},"html_url":"https://github.com/o/r/issues/3","created_at":"2026-07-04T10:00:00Z","updated_at":"2026-07-04T10:00:00Z"}
`
const pullsJSONL = `{"id":803,"number":4,"title":"Masih terbuka","state":"open","merge_commit_sha":"testmerge1","head":{"ref":"t"},"base":{"ref":"main"},"user":{"login":"bot"},"html_url":"https://github.com/o/r/pull/4","created_at":"2026-07-06T10:00:00Z","updated_at":"2026-07-06T10:00:00Z"}
{"id":802,"number":2,"title":"Naik taraf","body":"","state":"closed","merged_at":"2026-07-05T10:00:00Z","merge_commit_sha":"abc123","head":{"ref":"naik"},"base":{"ref":"main"},"user":{"login":"bot"},"html_url":"https://github.com/o/r/pull/2","created_at":"2026-07-03T10:00:00Z","updated_at":"2026-07-05T10:00:00Z","closed_at":"2026-07-05T10:00:00Z"}
`
const commentsJSONL = `{"id":701,"body":"Sahkan dahulu","user":{"login":"siti"},"html_url":"https://github.com/o/r/issues/1#c701","issue_url":"https://api.github.com/repos/o/r/issues/1","created_at":"2026-07-01T11:00:00Z"}
{"id":702,"body":"LGTM","user":{"login":"siti"},"html_url":"https://github.com/o/r/pull/2#c702","issue_url":"https://api.github.com/repos/o/r/issues/2","created_at":"2026-07-04T11:00:00Z"}
`

func setup(t *testing.T) (*store.Store, int64, int64) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(ctx, "admin", "", "h", true, "en-US")
	r, _ := st.CreateRepo(ctx, u.ID, "r", "", false, "main")
	return st, r.ID, u.ID
}

func TestImportDir(t *testing.T) {
	st, repo, by := setup(t)
	ctx := context.Background()
	// a native Codr issue already holds #3: the GitHub #3 must be skipped, never renumbered
	if _, err := st.CreateIssue(ctx, repo, by, "a", ""); err != nil { // #1
		t.Fatal(err)
	}
	_, _ = st.DB().Exec(`UPDATE issues SET number = 3 WHERE repo_id = ?`, repo)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "issues.jsonl"), []byte(issuesJSONL), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pulls.jsonl"), []byte(pullsJSONL), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "issue_comments.jsonl"), []byte(commentsJSONL), 0o600)
	res, err := Import(ctx, st, repo, by, Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if res.Issues != 1 || res.Pulls != 2 || res.Comments != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "#3" {
		t.Fatalf("first import: %+v", res)
	}
	i, err := st.IssueByNumber(ctx, repo, 1)
	if err != nil || i.Author != "ali" || i.ExtURL != "https://github.com/o/r/issues/1" || i.State != "closed" || i.CreatedAt != "2026-07-01T10:00:00.000Z" || len(i.Labels) != 1 || i.Labels[0].Name != "bug" {
		t.Fatalf("issue: %+v %v", i, err)
	}
	p, err := st.PullByNumber(ctx, repo, 2)
	if err != nil || p.State != "merged" || p.MergeCommit != "abc123" || p.Author != "bot" || p.HeadBranch != "naik" {
		t.Fatalf("pull: %+v %v", p, err)
	}
	if cs, _ := st.Comments(ctx, i.ID); len(cs) != 1 || cs[0].Author != "siti" || !strings.HasSuffix(cs[0].ExtURL, "#c701") {
		t.Fatalf("issue comment: %+v", cs)
	}
	if cs, _ := st.PullComments(ctx, p.ID); len(cs) != 1 || cs[0].Body != "LGTM" {
		t.Fatalf("pull comment routed wrong: %+v", cs)
	}
	if open, _ := st.PullByNumber(ctx, repo, 4); open.State != "open" || open.MergeCommit != "" {
		t.Fatalf("an open pull must not carry GitHub's test-merge commit: %+v", open)
	}
	again, err := Import(ctx, st, repo, by, Dir(dir))
	if err != nil || again.Issues != 0 || again.Pulls != 0 || again.Comments != 0 || again.Updated != 3 {
		t.Fatalf("second import must change nothing new: %+v %v", again, err)
	}
	// the next native item continues after the imported numbers
	if n, _ := st.CreateIssue(ctx, repo, by, "baharu", ""); n.Number != 5 {
		t.Fatalf("next native number %d", n.Number)
	}
}

func TestImportAPI(t *testing.T) {
	st, repo, by := setup(t)
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOK" || r.URL.Query().Get("state") != "all" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/repos/o/r/issues":
			pages++
			_, _ = w.Write([]byte("[" + strings.ReplaceAll(strings.TrimSpace(issuesJSONL), "\n", ",") + "]"))
		case "/repos/o/r/pulls":
			_, _ = w.Write([]byte("[" + strings.ReplaceAll(strings.TrimSpace(pullsJSONL), "\n", ",") + "]"))
		case "/repos/o/r/issues/comments":
			_, _ = w.Write([]byte("[" + strings.ReplaceAll(strings.TrimSpace(commentsJSONL), "\n", ",") + "]"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	res, err := Import(context.Background(), st, repo, by, API{BaseURL: srv.URL, Owner: "o", Repo: "r", Token: "TOK"})
	if err != nil || res.Issues != 2 || res.Pulls != 2 || res.Comments != 2 || pages != 1 {
		t.Fatalf("api import: %+v %v (pages %d)", res, err, pages)
	}
	if _, err := Import(context.Background(), st, repo, by, API{BaseURL: srv.URL, Owner: "o", Repo: "r", Token: "salah"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("bad token: %v", err)
	}
}

func TestParseGitHubURL(t *testing.T) {
	for in, want := range map[string]string{"https://github.com/aimanmalib/ade.git": "aimanmalib/ade", "https://github.com/a/b": "a/b", "https://gitlab.com/a/b": "/", "file:///cermin/x.git": "/", "https://github.com/a": "/"} {
		o, r := ParseGitHubURL(in)
		if o+"/"+r != want {
			t.Errorf("%s: %s/%s", in, o, r)
		}
	}
}
