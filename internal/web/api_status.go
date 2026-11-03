package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/store"
)

// Commit statuses (D81): what a CI runner reports back after a webhook told it about a push. Write access with a
// repo:write token; the commit must exist in the repository. The page shows them; webhooks get a "status" event.

var (
	shaRE     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	contextRE = regexp.MustCompile(`^[\pL\pN][\pL\pN ._:/()-]{0,99}$`)
)

func (s *Server) statusJSON(st store.CommitStatus) map[string]any {
	return map[string]any{"id": st.ID, "sha": st.SHA, "context": st.Context, "state": st.State, "description": st.Description,
		"target_url": st.TargetURL, "creator": st.Creator, "created_at": ts(st.CreatedAt), "updated_at": ts(st.UpdatedAt)}
}

func (s *Server) apiStatuses(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, _, ok := s.apiOpen(w, r, c)
	if !ok {
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))
	if !shaRE.MatchString(sha) {
		if full, err := s.git(rp.ID).Resolve(r.Context(), sha); err == nil {
			sha = full // a branch or tag name resolves to its commit
		} else {
			apiFail(w, http.StatusNotFound, "not_found", "no such commit")
			return
		}
	}
	sts, err := s.Store.Statuses(r.Context(), rp.ID, sha)
	if err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	list := make([]map[string]any, 0, len(sts))
	for _, st := range sts {
		list = append(list, s.statusJSON(st))
	}
	state := store.Combined(sts)
	if state == "" {
		state = "none"
	}
	apiJSON(w, http.StatusOK, map[string]any{"sha": sha, "state": state, "statuses": list})
}

func (s *Server) apiStatusCreate(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, ok := s.apiOpen(w, r, c)
	if !ok || !writeOK(w, c) {
		return
	}
	if a < store.AccessWrite {
		apiFail(w, http.StatusForbidden, "forbidden", "setting a status needs write access to the repository")
		return
	}
	sha := strings.ToLower(r.PathValue("sha"))
	if !shaRE.MatchString(sha) {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "sha must be a full 40-character commit id")
		return
	}
	if _, err := s.git(rp.ID).Commit(r.Context(), sha); err != nil {
		apiFail(w, http.StatusNotFound, "not_found", "no such commit in this repository")
		return
	}
	var in struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}
	if !readBody(w, r, 16<<10, &in) {
		return
	}
	if in.Context == "" {
		in.Context = "default"
	}
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case in.State != "pending" && in.State != "success" && in.State != "failure" && in.State != "error":
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "state is pending, success, failure or error")
		return
	case !contextRE.MatchString(in.Context):
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "context: up to 100 letters, digits, spaces and ._:/()-")
		return
	case utf8.RuneCountInString(in.Description) > 200 || strings.ContainsAny(in.Description, "\r\n"):
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "description: one line, at most 200 characters")
		return
	}
	if in.TargetURL != "" {
		u, err := url.Parse(in.TargetURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(in.TargetURL) > 2000 {
			apiFail(w, http.StatusUnprocessableEntity, "invalid", "target_url must be an http(s) URL")
			return
		}
	}
	ctx := r.Context()
	if n, _ := s.Store.CountStatuses(ctx, rp.ID, sha); n >= int(s.settingInt(ctx, "api.statuses_per_sha", 50)) {
		if existing, _ := s.Store.Statuses(ctx, rp.ID, sha); !hasContext(existing, in.Context) {
			apiFail(w, http.StatusUnprocessableEntity, "too_many", "this commit already has the most statuses allowed (api.statuses_per_sha)")
			return
		}
	}
	st := store.CommitStatus{SHA: sha, Context: in.Context, State: in.State, Description: in.Description, TargetURL: in.TargetURL}
	if err := s.Store.SetStatus(ctx, rp.ID, c.User.ID, st); err != nil {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.emit(ctx, rp.ID, c.User.ID, "status", map[string]any{"sha": sha, "context": in.Context, "state": in.State,
		"description": in.Description, "target_url": in.TargetURL})
	sts, _ := s.Store.Statuses(ctx, rp.ID, sha)
	for _, x := range sts {
		if x.Context == in.Context {
			apiJSON(w, http.StatusCreated, s.statusJSON(x))
			return
		}
	}
	apiJSON(w, http.StatusCreated, s.statusJSON(st))
}

func hasContext(sts []store.CommitStatus, c string) bool {
	for _, s := range sts {
		if s.Context == c {
			return true
		}
	}
	return false
}
