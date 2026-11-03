package web

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/version"
)

// REST API v1 (D81). Tokens only (Authorization: Bearer codr_…, or Basic user:token as git uses), never the session
// cookie: a cookie-authenticated write would be open to cross-site requests, and the API has no CSRF token. No
// header = anonymous, which sees what an anonymous visitor sees. What a call may do = the person's access, capped
// by the token's scope. Every route is listed once in apiRoutes; openapi.json documents exactly that list (tested).

//go:embed openapi.json
var openAPI []byte

type apiRoute struct {
	Method, Path string
	h            func(*Server, http.ResponseWriter, *http.Request, apiCaller)
}

var apiRoutes = []apiRoute{
	{"GET", "/api/v1/user", (*Server).apiMe},
	{"GET", "/api/v1/repos", (*Server).apiRepos},
	{"GET", "/api/v1/repos/{owner}/{repo}", (*Server).apiRepo},
	{"GET", "/api/v1/repos/{owner}/{repo}/branches", (*Server).apiBranches},
	{"GET", "/api/v1/repos/{owner}/{repo}/tags", (*Server).apiTags},
	{"GET", "/api/v1/repos/{owner}/{repo}/commits", (*Server).apiCommits},
	{"GET", "/api/v1/repos/{owner}/{repo}/contents/{path...}", (*Server).apiContents},
	{"GET", "/api/v1/repos/{owner}/{repo}/issues", (*Server).apiIssues},
	{"POST", "/api/v1/repos/{owner}/{repo}/issues", (*Server).apiIssueCreate},
	{"GET", "/api/v1/repos/{owner}/{repo}/issues/{n}", (*Server).apiIssue},
	{"PATCH", "/api/v1/repos/{owner}/{repo}/issues/{n}", (*Server).apiIssueEdit},
	{"GET", "/api/v1/repos/{owner}/{repo}/issues/{n}/comments", (*Server).apiIssueComments},
	{"POST", "/api/v1/repos/{owner}/{repo}/issues/{n}/comments", (*Server).apiIssueComment},
	{"POST", "/api/v1/repos/{owner}/{repo}/issues/{n}/assignees", (*Server).apiAssignees},
	{"DELETE", "/api/v1/repos/{owner}/{repo}/issues/{n}/assignees", (*Server).apiAssignees},
	{"GET", "/api/v1/repos/{owner}/{repo}/pulls", (*Server).apiPulls},
	{"GET", "/api/v1/repos/{owner}/{repo}/pulls/{n}", (*Server).apiPull},
	{"GET", "/api/v1/repos/{owner}/{repo}/commits/{sha}/statuses", (*Server).apiStatuses},
	{"POST", "/api/v1/repos/{owner}/{repo}/statuses/{sha}", (*Server).apiStatusCreate},
}

func (s *Server) registerAPI(mux *http.ServeMux) {
	for _, rt := range apiRoutes {
		h := rt.h
		mux.HandleFunc(rt.Method+" "+rt.Path, func(w http.ResponseWriter, r *http.Request) {
			c, ok := s.apiAuth(w, r)
			if ok {
				h(s, w, r, c)
			}
		})
	}
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPI)
	})
}

// apiCaller is who is calling: nil user = anonymous.
type apiCaller struct {
	User  *store.User
	Token *store.AccessToken
}

func (c apiCaller) id() int64 {
	if c.User == nil {
		return 0
	}
	return c.User.ID
}

type apiErr struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func apiJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-transform") // answers may hold private code
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func apiFail(w http.ResponseWriter, code int, slug, msg string) {
	apiJSON(w, code, apiErr{Error: slug, Message: msg})
}

func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) (apiCaller, bool) {
	ip := s.clientIP(r)
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		apiFail(w, http.StatusTooManyRequests, "rate_limited", "too many failed sign-ins from this address; wait and try again")
		return apiCaller{}, false
	}
	tok, u, err := s.authenticate(r.Context(), r)
	if err != nil {
		if s.Limiter != nil {
			s.Limiter.Fail(ip)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="codr"`)
		apiFail(w, http.StatusUnauthorized, "bad_credentials", "use a Codr access token: Authorization: Bearer codr_…")
		return apiCaller{}, false
	}
	return apiCaller{User: u, Token: tok}, true
}

// access is the person's access to a repository, capped by the token's scope.
func (s *Server) apiAccess(r *http.Request, c apiCaller, repo store.Repo) (store.Access, error) {
	a, err := s.Store.AccessFor(r.Context(), c.id(), repo)
	if err != nil || c.Token == nil {
		return a, err
	}
	for a > store.AccessNone && !c.Token.Allows(a) {
		a--
	}
	return a, nil
}

// apiOpen finds a repository the caller may at least read (404 otherwise, never 403: existence is not revealed).
func (s *Server) apiOpen(w http.ResponseWriter, r *http.Request, c apiCaller) (store.Repo, store.Access, bool) {
	repo, err := s.Store.RepoByPath(r.Context(), r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git"))
	a := store.AccessNone
	if err == nil {
		a, err = s.apiAccess(r, c, repo)
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return repo, a, false
	}
	if a < store.AccessRead {
		apiFail(w, http.StatusNotFound, "not_found", "repository not found")
		return repo, a, false
	}
	return repo, a, true
}

// writeOK checks a write: a token with repo:write (not a read-only token), and a person signed in.
func writeOK(w http.ResponseWriter, c apiCaller) bool {
	if c.User == nil {
		apiFail(w, http.StatusUnauthorized, "auth_required", "this call needs an access token")
		return false
	}
	if c.Token != nil && !c.Token.Allows(store.AccessWrite) {
		apiFail(w, http.StatusForbidden, "scope", "this token can only read; make one with repo:write")
		return false
	}
	return true
}

// page reads ?page and ?per_page (setting api.per_page_max bounds it).
func (s *Server) apiPage(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("per_page"))
	maxN := int(s.settingInt(r.Context(), "api.per_page_max", 100))
	if limit < 1 {
		limit = int(s.settingInt(r.Context(), "api.per_page", 30))
	}
	limit = min(limit, maxN)
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	return limit, (p - 1) * limit
}

func setTotal(w http.ResponseWriter, n int) { w.Header().Set("X-Total-Count", strconv.Itoa(n)) }

type apiUser struct {
	ID      int64  `json:"id"`
	Login   string `json:"login"`
	HTMLURL string `json:"html_url"`
}

func (s *Server) userJSON(id int64, name string) *apiUser {
	if name == "" {
		return nil
	}
	return &apiUser{ID: id, Login: name, HTMLURL: s.UI.HTTPHost + "/" + name}
}

func ts(s string) string {
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request, c apiCaller) {
	if c.User == nil {
		apiFail(w, http.StatusUnauthorized, "auth_required", "this call needs an access token")
		return
	}
	tf, _ := s.Store.TwoFactor(r.Context(), c.User.ID)
	var scopes []string
	if c.Token != nil {
		scopes = c.Token.Scopes
	}
	apiJSON(w, http.StatusOK, map[string]any{"id": c.User.ID, "login": c.User.Name, "email": c.User.Email, "site_admin": c.User.IsAdmin,
		"two_factor": tf.On != "", "html_url": s.UI.HTTPHost + "/" + c.User.Name, "token_scopes": scopes, "codr_version": version.Tag})
}

type apiRepoJSON struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Owner         string `json:"owner"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Mirror        bool   `json:"mirror"`
	Fork          bool   `json:"fork"`
	DefaultBranch string `json:"default_branch"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
	SSHURL        string `json:"ssh_url"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Permission    string `json:"permission,omitempty"`
}

func (s *Server) repoJSON(rp store.Repo, a store.Access) apiRepoJSON {
	full := rp.Owner + "/" + rp.Name
	out := apiRepoJSON{ID: rp.ID, Name: rp.Name, FullName: full, Owner: rp.Owner, Description: rp.Description, Private: rp.Private,
		Archived: rp.Archived, Mirror: rp.Mirror, Fork: rp.ForkOf > 0, DefaultBranch: rp.DefaultBranch, HTMLURL: s.UI.HTTPHost + "/" + full,
		CloneURL: s.UI.HTTPHost + "/" + full + ".git", SSHURL: "ssh://git@" + s.UI.SSHHost + "/" + full + ".git",
		CreatedAt: ts(rp.CreatedAt), UpdatedAt: ts(rp.UpdatedAt)}
	if a > store.AccessNone {
		out.Permission = a.String()
	}
	return out
}

func (s *Server) apiRepos(w http.ResponseWriter, r *http.Request, c apiCaller) {
	limit, offset := s.apiPage(r)
	q := r.URL.Query()
	list, total, err := s.Store.ReposVisible(r.Context(), c.id(), q.Get("owner"), q.Get("q"), "recent", limit, offset)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]apiRepoJSON, 0, len(list))
	for _, rp := range list {
		a, _ := s.apiAccess(r, c, rp)
		if a < store.AccessRead {
			continue // a read-only token may not list what its scope cannot open
		}
		out = append(out, s.repoJSON(rp, a))
	}
	setTotal(w, total)
	apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiRepo(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, ok := s.apiOpen(w, r, c)
	if ok {
		apiJSON(w, http.StatusOK, s.repoJSON(rp, a))
	}
}

func (s *Server) apiRefs(w http.ResponseWriter, r *http.Request, c apiCaller, tags bool) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	g := s.git(rp.ID)
	var refs []gitread.Ref
	var err error
	if tags {
		refs, err = g.Tags(r.Context())
	} else {
		refs, err = g.Branches(r.Context())
	}
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(refs))
	for _, rf := range refs {
		out = append(out, map[string]any{"name": rf.Name, "commit": map[string]string{"sha": rf.Hash, "url": s.UI.HTTPHost + "/" + rp.Owner + "/" + rp.Name + "/commit/" + rf.Hash}})
	}
	apiJSON(w, http.StatusOK, out)
}

func (s *Server) apiBranches(w http.ResponseWriter, r *http.Request, c apiCaller) {
	s.apiRefs(w, r, c, false)
}
func (s *Server) apiTags(w http.ResponseWriter, r *http.Request, c apiCaller) {
	s.apiRefs(w, r, c, true)
}

func refOr(r *http.Request, rp store.Repo) string {
	if ref := r.URL.Query().Get("ref"); ref != "" {
		return ref
	}
	return rp.DefaultBranch
}

func (s *Server) commitJSON(rp store.Repo, cm gitread.Commit) map[string]any {
	return map[string]any{"sha": cm.Hash, "parents": cm.Parents, "message": strings.TrimSpace(cm.Subject + "\n\n" + cm.Body),
		"author":   map[string]string{"name": cm.Author, "email": cm.Email, "date": cm.When.UTC().Format(time.RFC3339)},
		"html_url": s.UI.HTTPHost + "/" + rp.Owner + "/" + rp.Name + "/commit/" + cm.Hash}
}

func (s *Server) apiCommits(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	limit, offset := s.apiPage(r)
	cs, err := s.git(rp.ID).Log(r.Context(), refOr(r, rp), r.URL.Query().Get("path"), limit, offset)
	if errors.Is(err, gitread.ErrBadRef) || errors.Is(err, gitread.ErrNotFound) {
		apiFail(w, http.StatusNotFound, "not_found", "no such ref or path")
		return
	} else if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, cm := range cs {
		out = append(out, s.commitJSON(rp, cm))
	}
	apiJSON(w, http.StatusOK, out)
}

// apiContents lists a directory or returns one file (base64), like GitHub's contents API, bounded by ui.blob_max_kb.
func (s *Server) apiContents(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	g, ref, p := s.git(rp.ID), refOr(r, rp), strings.Trim(r.PathValue("path"), "/")
	ents, err := g.Tree(r.Context(), ref, p)
	if err == nil {
		out := make([]map[string]any, 0, len(ents))
		for _, e := range ents {
			typ := "file"
			if e.IsDir() {
				typ = "dir"
			}
			out = append(out, map[string]any{"name": e.Name, "path": strings.TrimPrefix(p+"/"+e.Name, "/"), "type": typ, "sha": e.Hash, "size": max(e.Size, 0)})
		}
		apiJSON(w, http.StatusOK, out)
		return
	}
	maxKB := s.settingInt(r.Context(), "ui.blob_max_kb", 1024)
	b, err := g.Blob(r.Context(), ref, p, maxKB<<10)
	if err != nil {
		apiFail(w, http.StatusNotFound, "not_found", "no such ref or path")
		return
	}
	if b.Truncated {
		apiFail(w, http.StatusRequestEntityTooLarge, "too_large", "file is larger than ui.blob_max_kb; clone the repository instead")
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"name": p[strings.LastIndex(p, "/")+1:], "path": p, "type": "file", "size": b.Size,
		"encoding": "base64", "content": base64.StdEncoding.EncodeToString(b.Content), "binary": b.Binary})
}
