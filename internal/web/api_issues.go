package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/store"
)

// Issues and pull requests over the API (D81). The same rules as the pages: who may open, comment or manage comes
// from apiIssueCan (the issueCan rule with the token cap), and every change writes the same audit row and outbox
// event as the page does, so webhooks and notifications fire for API calls too.

// apiIssueCan is issueCan for an API caller.
func (s *Server) apiIssueCan(r *http.Request, c apiCaller, rp store.Repo, a store.Access) (create, manage bool) {
	if c.User == nil || rp.Archived {
		return false, false
	}
	manage = a >= store.AccessWrite
	switch {
	case manage:
		create = true
	case rp.Private:
		create = a >= store.AccessRead
	default:
		create, _ = s.Store.SettingBool(r.Context(), "issue.public_create")
	}
	return create, manage
}

func (s *Server) issueJSON(ctx context.Context, rp store.Repo, i store.Issue) map[string]any {
	labels := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, l.Name)
	}
	out := map[string]any{"number": i.Number, "title": i.Title, "body": i.Body, "state": i.State, "user": s.userJSON(i.AuthorID, i.Author),
		"labels": labels, "assignees": s.assigneesJSON(ctx, rp.ID, i.Number), "comments": i.Comments, "created_at": ts(i.CreatedAt), "updated_at": ts(i.UpdatedAt),
		"html_url": s.UI.HTTPHost + "/" + rp.Owner + "/" + rp.Name + "/isu/" + strconv.FormatInt(i.Number, 10)}
	if i.ClosedAt != "" {
		out["closed_at"] = ts(i.ClosedAt)
	}
	return out
}

func readBody(w http.ResponseWriter, r *http.Request, limit int, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)+8192)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		apiFail(w, http.StatusBadRequest, "bad_json", "the body must be a JSON object")
		return false
	}
	return true
}

func (s *Server) apiIssues(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "all" {
		state = "open"
	}
	limit, offset := s.apiPage(r)
	assignee, _ := s.assigneeFilter(r, "assignee")
	list, total, err := s.Store.Issues(r.Context(), rp.ID, state, 0, r.URL.Query().Get("q"), assignee, limit, offset)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, i := range list {
		out = append(out, s.issueJSON(r.Context(), rp, i))
	}
	setTotal(w, total)
	apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiOpenIssue(w http.ResponseWriter, r *http.Request, c apiCaller) (store.Repo, store.Access, store.Issue, bool) {
	rp, a, ok := s.apiOpen(w, r, c)
	if !ok {
		return rp, a, store.Issue{}, false
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	i, err := s.Store.IssueByNumber(r.Context(), rp.ID, n)
	if err != nil {
		apiFail(w, http.StatusNotFound, "not_found", "issue not found")
		return rp, a, i, false
	}
	return rp, a, i, true
}

func (s *Server) apiIssue(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, i, ok := s.apiOpenIssue(w, r, c)
	if ok {
		apiJSON(w, http.StatusOK, s.issueJSON(r.Context(), rp, i))
	}
}

type issueIn struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
	State *string `json:"state"`
}

func validTitle(t string) bool {
	t = strings.TrimSpace(t)
	return t != "" && utf8.RuneCountInString(t) <= 200 && !strings.ContainsAny(t, "\r\n")
}

func (s *Server) apiIssueCreate(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, ok := s.apiOpen(w, r, c)
	if !ok || !writeOK(w, c) {
		return
	}
	if create, _ := s.apiIssueCan(r, c, rp, a); !create {
		apiFail(w, http.StatusForbidden, "forbidden", "you may not open issues here")
		return
	}
	var in issueIn
	if !readBody(w, r, s.bodyLimit(r), &in) {
		return
	}
	body := ""
	if in.Body != nil {
		body = strings.TrimSpace(*in.Body)
	}
	if in.Title == nil || !validTitle(*in.Title) || len(body) > s.bodyLimit(r) {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "title is required (one line, at most 200 characters); body is bounded by issue.body_max_kb")
		return
	}
	i, err := s.Store.CreateIssue(r.Context(), rp.ID, c.User.ID, strings.TrimSpace(*in.Title), body)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	i.Author = c.User.Name
	_ = s.Store.Audit(r.Context(), c.User.ID, "issue.open", rp.Owner+"/"+rp.Name+"#"+strconv.FormatInt(i.Number, 10), i.Title+" (api)")
	s.emit(r.Context(), rp.ID, c.User.ID, "issues", issueEvent(i, "opened"))
	apiJSON(w, http.StatusCreated, s.issueJSON(r.Context(), rp, i))
}

func (s *Server) apiIssueEdit(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, i, ok := s.apiOpenIssue(w, r, c)
	if !ok || !writeOK(w, c) {
		return
	}
	_, manage := s.apiIssueCan(r, c, rp, a)
	if rp.Archived || (!manage && i.AuthorID != c.User.ID) { // the author may edit and close their own issue, as on the page
		apiFail(w, http.StatusForbidden, "forbidden", "only the author or someone with write access may change this issue")
		return
	}
	var in issueIn
	if !readBody(w, r, s.bodyLimit(r), &in) {
		return
	}
	ctx, target := r.Context(), rp.Owner+"/"+rp.Name+"#"+strconv.FormatInt(i.Number, 10)
	if in.Title != nil || in.Body != nil {
		title, body := i.Title, i.Body
		if in.Title != nil {
			title = strings.TrimSpace(*in.Title)
		}
		if in.Body != nil {
			body = strings.TrimSpace(*in.Body)
		}
		if !validTitle(title) || len(body) > s.bodyLimit(r) {
			apiFail(w, http.StatusUnprocessableEntity, "invalid", "title must be one line of at most 200 characters")
			return
		}
		if err := s.Store.UpdateIssue(ctx, i.ID, title, body); err != nil {
			apiFail(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		i.Title, i.Body = title, body
		_ = s.Store.Audit(ctx, c.User.ID, "issue.edit", target, title+" (api)")
		s.emit(ctx, rp.ID, c.User.ID, "issues", issueEvent(i, "edited"))
	}
	if in.State != nil && *in.State != i.State {
		closed := *in.State == "closed"
		if !closed && *in.State != "open" {
			apiFail(w, http.StatusUnprocessableEntity, "invalid", "state is open or closed")
			return
		}
		if err := s.Store.SetIssueState(ctx, i.ID, closed); err != nil {
			apiFail(w, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		action := map[bool]string{true: "closed", false: "reopened"}[closed]
		_ = s.Store.Audit(ctx, c.User.ID, map[bool]string{true: "issue.close", false: "issue.reopen"}[closed], target, "api")
		s.emit(ctx, rp.ID, c.User.ID, "issues", issueEvent(i, action))
	}
	i, _ = s.Store.IssueByNumber(ctx, rp.ID, i.Number)
	apiJSON(w, http.StatusOK, s.issueJSON(r.Context(), rp, i))
}

func (s *Server) commentJSON(rp store.Repo, c store.Comment, path string) map[string]any {
	return map[string]any{"id": c.ID, "user": s.userJSON(c.AuthorID, c.Author), "body": c.Body, "created_at": ts(c.CreatedAt),
		"html_url": s.UI.HTTPHost + "/" + rp.Owner + "/" + rp.Name + path + "#komen-" + strconv.FormatInt(c.ID, 10)}
}

func (s *Server) apiIssueComments(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, i, ok := s.apiOpenIssue(w, r, c)
	if !ok {
		return
	}
	cs, err := s.Store.Comments(r.Context(), i.ID)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, cm := range cs {
		out = append(out, s.commentJSON(rp, cm, "/isu/"+strconv.FormatInt(i.Number, 10)))
	}
	apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiIssueComment(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, i, ok := s.apiOpenIssue(w, r, c)
	if !ok || !writeOK(w, c) {
		return
	}
	if create, _ := s.apiIssueCan(r, c, rp, a); !create {
		apiFail(w, http.StatusForbidden, "forbidden", "you may not comment here")
		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if !readBody(w, r, s.bodyLimit(r), &in) {
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > s.bodyLimit(r) {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "body is required and bounded by issue.body_max_kb")
		return
	}
	cm, err := s.Store.AddComment(r.Context(), i.ID, c.User.ID, body)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	cm.Author = c.User.Name
	s.emit(r.Context(), rp.ID, c.User.ID, "issue_comment", map[string]any{"action": "created", "id": cm.ID, "number": i.Number,
		"is_pull": false, "title": i.Title, "body": body, "user": c.User.Name})
	apiJSON(w, http.StatusCreated, s.commentJSON(rp, cm, "/isu/"+strconv.FormatInt(i.Number, 10)))
}

func (s *Server) pullJSON(ctx context.Context, rp store.Repo, p store.Pull) map[string]any {
	out := map[string]any{"number": p.Number, "title": p.Title, "body": p.Body, "state": p.State, "merged": p.State == "merged",
		"user": s.userJSON(p.AuthorID, p.Author), "head": map[string]string{"ref": p.HeadBranch}, "base": map[string]string{"ref": p.BaseBranch},
		"assignees": s.assigneesJSON(ctx, rp.ID, p.Number), "comments": p.Comments, "created_at": ts(p.CreatedAt), "updated_at": ts(p.UpdatedAt),
		"html_url": s.UI.HTTPHost + "/" + rp.Owner + "/" + rp.Name + "/pr/" + strconv.FormatInt(p.Number, 10)}
	if p.MergeCommit != "" {
		out["merge_commit_sha"] = p.MergeCommit
	}
	return out
}

func (s *Server) apiPulls(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "merged" && state != "all" {
		state = "open"
	}
	limit, offset := s.apiPage(r)
	assignee, _ := s.assigneeFilter(r, "assignee")
	list, total, err := s.Store.Pulls(r.Context(), rp.ID, state, r.URL.Query().Get("q"), assignee, limit, offset)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, p := range list {
		out = append(out, s.pullJSON(r.Context(), rp, p))
	}
	setTotal(w, total)
	apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiPull(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	p, err := s.Store.PullByNumber(r.Context(), rp.ID, n)
	if errors.Is(err, store.ErrNotFound) {
		apiFail(w, http.StatusNotFound, "not_found", "pull request not found")
		return
	} else if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	apiJSON(w, http.StatusOK, s.pullJSON(r.Context(), rp, p))
}
