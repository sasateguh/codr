// Package ghimport brings a GitHub repository's issues, pull requests and comments into Codr (D72), from either
// the GitHub REST API (a mirror imported from github.com with its token) or the JSON Lines files the host's
// GitHub mirror keeps (issues.jsonl, pulls.jsonl, issue_comments.jsonl). Both are the same GitHub objects.
package ghimport

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/store"
)

type user struct {
	Login string `json:"login"`
}

type label struct {
	Name string `json:"name"`
}

type ref struct {
	Ref string `json:"ref"`
}

// Issue is the part of a GitHub issue (or pull request) object Codr keeps.
type Issue struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	Body        *string         `json:"body"`
	State       string          `json:"state"`
	User        user            `json:"user"`
	HTMLURL     string          `json:"html_url"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
	ClosedAt    *string         `json:"closed_at"`
	Labels      []label         `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"` // present when an issues listing returns a pull request
	MergedAt    *string         `json:"merged_at"`
	MergeSHA    *string         `json:"merge_commit_sha"`
	Head        ref             `json:"head"`
	Base        ref             `json:"base"`
}

// Comment is a GitHub issue comment (pull request conversations use the same objects).
type Comment struct {
	ID        int64   `json:"id"`
	Body      *string `json:"body"`
	User      user    `json:"user"`
	HTMLURL   string  `json:"html_url"`
	IssueURL  string  `json:"issue_url"`
	CreatedAt string  `json:"created_at"`
}

// Source yields the three lists.
type Source interface {
	Issues(ctx context.Context) ([]Issue, error)
	Pulls(ctx context.Context) ([]Issue, error)
	Comments(ctx context.Context) ([]Comment, error)
}

// Result counts what an import did.
type Result struct {
	Issues, Pulls, Comments int // new rows
	Updated                 int // existing rows refreshed
	Skipped                 []string
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// codrTime turns GitHub's RFC 3339 into the store's format; "" stays "".
func codrTime(v string) string {
	if v == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// mergeCommit keeps GitHub's merge_commit_sha only for a merged pull request: for an open one it is the
// test-merge commit GitHub computes, which never landed anywhere.
func mergeCommit(state string, sha *string) string {
	if state != "merged" {
		return ""
	}
	return str(sha)
}

// safeURL keeps a link only when it is https (the data comes from outside; the template escapes it too).
func safeURL(v string) string {
	if strings.HasPrefix(v, "https://") && !strings.ContainsAny(v, " \"<>") {
		return v
	}
	return ""
}

func issueNumber(issueURL string) (int64, bool) {
	i := strings.LastIndex(issueURL, "/")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(issueURL[i+1:], 10, 64)
	return n, err == nil && n > 0
}

// Import writes src into repoID; by is the importing admin. Re-running is safe (D72).
func Import(ctx context.Context, st *store.Store, repoID, by int64, src Source) (Result, error) {
	var res Result
	issues, err := src.Issues(ctx)
	if err != nil {
		return res, err
	}
	pulls, err := src.Pulls(ctx)
	if err != nil {
		return res, err
	}
	comments, err := src.Comments(ctx)
	if err != nil {
		return res, err
	}
	isPull := map[int64]bool{}
	for _, p := range pulls {
		isPull[p.Number] = true
	}
	for _, it := range issues {
		if len(it.PullRequest) > 0 && string(it.PullRequest) != "null" || isPull[it.Number] {
			continue // pull requests come from the pulls list, with their branches
		}
		names := make([]string, 0, len(it.Labels))
		for _, l := range it.Labels {
			names = append(names, l.Name)
		}
		state := "open"
		if it.State == "closed" {
			state = "closed"
		}
		_, created, err := st.UpsertImportedIssue(ctx, repoID, by, store.Imported{ExtID: it.ID, Number: it.Number, Title: it.Title, Body: str(it.Body), State: state,
			Author: it.User.Login, URL: safeURL(it.HTMLURL), CreatedAt: codrTime(it.CreatedAt), UpdatedAt: codrTime(it.UpdatedAt), ClosedAt: codrTime(str(it.ClosedAt)), Labels: names})
		switch {
		case errors.Is(err, store.ErrNumberTaken):
			res.Skipped = append(res.Skipped, "#"+strconv.FormatInt(it.Number, 10))
		case err != nil:
			return res, fmt.Errorf("issue #%d: %w", it.Number, err)
		case created:
			res.Issues++
		default:
			res.Updated++
		}
	}
	for _, p := range pulls {
		state := "open"
		switch {
		case str(p.MergedAt) != "":
			state = "merged"
		case p.State == "closed":
			state = "closed"
		}
		_, created, err := st.UpsertImportedPull(ctx, repoID, by, store.Imported{ExtID: p.ID, Number: p.Number, Title: p.Title, Body: str(p.Body), State: state,
			Author: p.User.Login, URL: safeURL(p.HTMLURL), CreatedAt: codrTime(p.CreatedAt), UpdatedAt: codrTime(p.UpdatedAt), ClosedAt: codrTime(str(p.ClosedAt)),
			Head: p.Head.Ref, Base: p.Base.Ref, MergeCommit: mergeCommit(state, p.MergeSHA)})
		switch {
		case errors.Is(err, store.ErrNumberTaken):
			res.Skipped = append(res.Skipped, "#"+strconv.FormatInt(p.Number, 10))
		case err != nil:
			return res, fmt.Errorf("pull #%d: %w", p.Number, err)
		case created:
			res.Pulls++
		default:
			res.Updated++
		}
	}
	for _, c := range comments {
		n, ok := issueNumber(c.IssueURL)
		if !ok {
			continue
		}
		var parent int64
		var err error
		if isPull[n] {
			parent, err = st.PullIDByNumber(ctx, repoID, n)
		} else {
			parent, err = st.IssueIDByNumber(ctx, repoID, n)
		}
		if err != nil {
			continue // its issue was skipped
		}
		added, err := st.AddImportedComment(ctx, isPull[n], parent, by, store.ImportedComment{ExtID: c.ID, Body: str(c.Body), Author: c.User.Login, URL: safeURL(c.HTMLURL), CreatedAt: codrTime(c.CreatedAt)})
		if err != nil {
			return res, fmt.Errorf("comment %d: %w", c.ID, err)
		}
		if added {
			res.Comments++
		}
	}
	return res, nil
}

// Dir reads the GitHub mirror's JSON Lines files of one repository; a missing file is an empty list.
type Dir string

func readLines[T any](path string) ([]T, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-configured mirror folder
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []T
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

func (d Dir) Issues(context.Context) ([]Issue, error) {
	return readLines[Issue](filepath.Join(string(d), "issues.jsonl"))
}
func (d Dir) Pulls(context.Context) ([]Issue, error) {
	return readLines[Issue](filepath.Join(string(d), "pulls.jsonl"))
}
func (d Dir) Comments(context.Context) ([]Comment, error) {
	return readLines[Comment](filepath.Join(string(d), "issue_comments.jsonl"))
}

// API reads from the GitHub REST API (BaseURL is https://api.github.com unless a test or GitHub Enterprise says
// otherwise). Token may be empty for a public repository.
type API struct {
	BaseURL, Owner, Repo, Token string
	HTTP                        *http.Client
	MaxPages                    int
}

func getAll[T any](ctx context.Context, a API, path string) ([]T, error) {
	base := a.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	max := a.MaxPages
	if max <= 0 {
		max = 100
	}
	var out []T
	for page := 1; page <= max; page++ {
		q := url.Values{"state": {"all"}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		u := strings.TrimRight(base, "/") + "/repos/" + url.PathEscape(a.Owner) + "/" + url.PathEscape(a.Repo) + path + "?" + q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		if a.Token != "" {
			req.Header.Set("Authorization", "Bearer "+a.Token)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			msg := string(body)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			return nil, fmt.Errorf("github %s: HTTP %d: %s", path, resp.StatusCode, msg)
		}
		var batch []T
		if err := json.Unmarshal(body, &batch); err != nil {
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

func (a API) Issues(ctx context.Context) ([]Issue, error) { return getAll[Issue](ctx, a, "/issues") }
func (a API) Pulls(ctx context.Context) ([]Issue, error)  { return getAll[Issue](ctx, a, "/pulls") }
func (a API) Comments(ctx context.Context) ([]Comment, error) {
	return getAll[Comment](ctx, a, "/issues/comments")
}

// ParseGitHubURL returns owner and repo of a github.com clone URL ("" when it is not one).
func ParseGitHubURL(raw string) (owner, repo string) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", ""
	}
	return parts[0], parts[1]
}
