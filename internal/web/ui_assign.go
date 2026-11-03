package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"codr.my/codr/internal/store"
)

// Assignees on issues and pull requests (D85, FR-21). One rule, here:
//   - assign / unassign: write access on the repository (the "manage" right of issueCan / pullCan)
//   - who may carry a thread: an active person (not an organisation) with write access, or with read access who already
//     took part in that thread (opened it, commented, reviewed). On a public repository read access alone is not
//     enough, otherwise anyone with an account could be dropped into a stranger's list.
//   - at most issue.max_assignees people per thread

// assigneeOK reports whether u may carry thread #number of rp.
func (s *Server) assigneeOK(ctx context.Context, rp store.Repo, thread string, number int64, u store.User) bool {
	if u.Disabled || u.IsOrg {
		return false
	}
	a, err := s.Store.AccessFor(ctx, u.ID, rp)
	if err != nil || a < store.AccessRead {
		return false
	}
	if a >= store.AccessWrite {
		return true
	}
	people, err := s.Store.ThreadParticipants(ctx, rp.ID, thread, number)
	if err != nil {
		return false
	}
	for _, id := range people {
		if id == u.ID {
			return true
		}
	}
	return false
}

// Outcomes of one assignment change, shared by the page and the API.
type assignOutcome int

const (
	assignDone assignOutcome = iota
	assignUnknown
	assignRefused
	assignFull
)

// applyAssign adds (or with remove, takes off) the person called name on thread #number of rp, as actor. The caller
// has already checked that actor may manage the thread. event builds the outbox payload for "assigned"/"unassigned";
// the audit row and the event are written only when something changed.
func (s *Server) applyAssign(ctx context.Context, rp store.Repo, actor *store.User, thread string, number int64, name string, remove bool,
	event func(action string) (string, map[string]any)) (store.User, assignOutcome, error) {
	u, err := s.Store.UserByName(ctx, strings.TrimSpace(name))
	if err != nil {
		return u, assignUnknown, nil
	}
	var changed bool
	action := "assigned"
	if remove {
		action = "unassigned"
		if changed, err = s.Store.Unassign(ctx, rp.ID, number, u.ID); err != nil {
			return u, assignDone, err
		}
	} else {
		if !s.assigneeOK(ctx, rp, thread, number, u) {
			return u, assignRefused, nil
		}
		max := s.settingInt(ctx, "issue.max_assignees", 10)
		n, err := s.Store.AssigneeCount(ctx, rp.ID, number)
		if err != nil {
			return u, assignDone, err
		}
		if int64(n) >= max {
			current, _ := s.Store.Assignees(ctx, rp.ID, number)
			for _, a := range current {
				if a.UserID == u.ID {
					return u, assignDone, nil // already on it: nothing to add, nothing over the limit
				}
			}
			return u, assignFull, nil
		}
		if changed, err = s.Store.Assign(ctx, rp.ID, number, u.ID, actor.ID); err != nil {
			return u, assignDone, err
		}
	}
	if changed {
		audit := map[bool]string{false: "assign.add", true: "assign.remove"}[remove]
		_ = s.Store.Audit(ctx, actor.ID, audit, rp.Owner+"/"+rp.Name+"#"+strconv.FormatInt(number, 10), u.Name)
		kind, data := event(action)
		data["assignee"], data["assignee_id"] = u.Name, u.ID
		s.emit(ctx, rp.ID, actor.ID, kind, data)
	}
	return u, assignDone, nil
}

// assign applies one assign/unassign form post and answers with a redirect back to the thread.
func (s *Server) assign(w http.ResponseWriter, r *http.Request, pg page, rc repoCtx, thread string, number int64, back string,
	event func(action string) (string, map[string]any)) {
	name := strings.TrimSpace(r.PostFormValue("nama"))
	remove := r.PostFormValue("tindakan") == "buang"
	u, out, err := s.applyAssign(r.Context(), rc.Repo, pg.User, thread, number, name, remove, event)
	switch {
	case err != nil:
		s.fail(w, r, err)
		return
	case out == assignUnknown:
		s.setFlash(w, "ralat", pg.T("ui.assign.unknown", name))
	case out == assignRefused:
		s.setFlash(w, "ralat", pg.T("ui.assign.not_allowed", u.Name))
	case out == assignFull:
		s.setFlash(w, "ralat", pg.T("ui.assign.too_many", s.settingInt(r.Context(), "issue.max_assignees", 10)))
	case remove:
		s.setFlash(w, "berjaya", pg.T("ui.assign.removed", u.Name))
	default:
		s.setFlash(w, "berjaya", pg.T("ui.assign.added", u.Name))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) uiIssueAssign(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, i, ok := s.openIssue(w, r, pg)
	if !ok {
		return
	}
	if _, manage := s.issueCan(r, pg, rc); !manage || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	s.assign(w, r, pg, rc, "issue", i.Number, s.issuesPath(rc)+"/"+strconv.FormatInt(i.Number, 10), func(action string) (string, map[string]any) {
		return "issues", issueEvent(i, action)
	})
}

func (s *Server) uiPullAssign(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	if _, manage := s.pullCan(pg, rc); !manage || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	s.assign(w, r, pg, rc, "pull", p.Number, s.pullsPath(rc)+"/"+strconv.FormatInt(p.Number, 10), func(action string) (string, map[string]any) {
		return "pull_request", pullEvent(p, "", action)
	})
}

// assigneeFilter reads ?<param>=<name> for a list: the user id to filter by (0 = no filter, -1 = unknown name, which
// lists nothing) and the name to keep in links.
func (s *Server) assigneeFilter(r *http.Request, param string) (int64, string) {
	name := strings.TrimSpace(r.URL.Query().Get(param))
	if name == "" {
		return 0, ""
	}
	u, err := s.Store.UserByName(r.Context(), name)
	if err != nil {
		return -1, name
	}
	return u.ID, u.Name
}

// listLink builds a list page query string, dropping empty values, so every filter survives paging and state switches.
func listLink(state string, label int64, q, assignee string, page int) string {
	v := url.Values{}
	v.Set("keadaan", state)
	if label > 0 {
		v.Set("label", strconv.FormatInt(label, 10))
	}
	if q != "" {
		v.Set("q", q)
	}
	if assignee != "" {
		v.Set("penerima", assignee)
	}
	if page > 1 {
		v.Set("muka", strconv.Itoa(page))
	}
	return "?" + v.Encode()
}
