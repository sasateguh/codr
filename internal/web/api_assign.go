package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"codr.my/codr/internal/store"
)

// Assignees over the API (D85). Like GitHub, issues/{n}/assignees serves pull requests too ("#n" is shared, D53).
// Managing needs write access capped by a repo:write token; the person rules are the page's (applyAssign).

func (s *Server) assigneesJSON(ctx context.Context, repoID, number int64) []*apiUser {
	as, _ := s.Store.Assignees(ctx, repoID, number)
	out := make([]*apiUser, 0, len(as))
	for _, a := range as {
		out = append(out, s.userJSON(a.UserID, a.Name))
	}
	return out
}

func (s *Server) apiAssignees(w http.ResponseWriter, r *http.Request, c apiCaller) {
	rp, a, ok := s.apiOpen(w, r, c)
	if !ok || !writeOK(w, c) {
		return
	}
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	var thread string
	var event func(string) (string, map[string]any)
	if i, err := s.Store.IssueByNumber(r.Context(), rp.ID, n); err == nil {
		thread = "issue"
		event = func(action string) (string, map[string]any) { return "issues", issueEvent(i, action) }
	} else if p, perr := s.Store.PullByNumber(r.Context(), rp.ID, n); perr == nil {
		thread = "pull"
		event = func(action string) (string, map[string]any) { return "pull_request", pullEvent(p, "", action) }
	} else if !errors.Is(err, store.ErrNotFound) || !errors.Is(perr, store.ErrNotFound) {
		apiFail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	} else {
		apiFail(w, http.StatusNotFound, "not_found", "issue not found")
		return
	}
	if _, manage := s.apiIssueCan(r, c, rp, a); !manage {
		apiFail(w, http.StatusForbidden, "forbidden", "assigning needs write access")
		return
	}
	var in struct {
		Assignees []string `json:"assignees"`
	}
	if !readBody(w, r, 64<<10, &in) {
		return
	}
	if len(in.Assignees) == 0 || int64(len(in.Assignees)) > s.settingInt(r.Context(), "issue.max_assignees", 10) {
		apiFail(w, http.StatusUnprocessableEntity, "invalid", "assignees must list one name or more, bounded by issue.max_assignees")
		return
	}
	remove := r.Method == http.MethodDelete
	for _, name := range in.Assignees {
		_, out, err := s.applyAssign(r.Context(), rp, c.User, thread, n, name, remove, event)
		switch {
		case err != nil:
			apiFail(w, http.StatusInternalServerError, "internal", "internal error")
			return
		case out == assignUnknown:
			apiFail(w, http.StatusUnprocessableEntity, "unknown_user", "no user named "+strconv.Quote(name))
			return
		case out == assignRefused:
			apiFail(w, http.StatusUnprocessableEntity, "not_assignable", name+" needs write access, or read access and a part in this thread")
			return
		case out == assignFull:
			apiFail(w, http.StatusUnprocessableEntity, "too_many", "at most issue.max_assignees assignees per thread")
			return
		}
	}
	code := http.StatusCreated
	if remove {
		code = http.StatusOK
	}
	apiJSON(w, code, map[string]any{"number": n, "assignees": s.assigneesJSON(r.Context(), rp.ID, n)})
}
