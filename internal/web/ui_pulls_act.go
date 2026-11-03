package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/store"
)

// Pull request writes: comment, review, close/reopen, edit, merge (D53). Each checks the
// permission in pullCan/pullState first and the CSRF token second.

func (s *Server) pullPath(rc repoCtx, p store.Pull) string {
	return s.pullsPath(rc) + "/" + strconv.FormatInt(p.Number, 10)
}

func (s *Server) uiPullComment(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	if pg.User == nil {
		http.Redirect(w, r, "/login?next="+s.pullPath(rc, p), http.StatusSeeOther)
		return
	}
	if can, _ := s.issueCan(r, pg, rc); !can || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	if body == "" || len(body) > s.bodyLimit(r) {
		s.renderPull(w, r, pg, g, rc, p, pullData{Error: pg.T("ui.issue.bad_body", s.bodyLimit(r)>>10), Draft: body}, http.StatusBadRequest)
		return
	}
	cid, err := s.Store.AddPullComment(r.Context(), p.ID, pg.User.ID, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "issue_comment", map[string]any{"action": "created", "id": cid.ID, "number": p.Number,
		"is_pull": true, "title": p.Title, "body": body, "user": pg.User.Name})
	s.setFlash(w, "berjaya", pg.T("ui.issue.commented"))
	http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
}

func (s *Server) uiPullReview(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	_, manage := s.pullCan(pg, rc)
	if pg.User == nil || !manage || pg.User.ID == p.AuthorID || !p.Open() || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	verdict := r.PostFormValue("verdict")
	body := strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n"))
	valid := false
	for _, v := range store.ReviewVerdicts {
		valid = valid || v == verdict
	}
	if !valid || len(body) > s.bodyLimit(r) || (verdict == "comment" && body == "") {
		s.renderPull(w, r, pg, g, rc, p, pullData{Error: pg.T("ui.pull.bad_review"), Draft: body}, http.StatusBadRequest)
		return
	}
	if _, err := s.Store.AddReview(r.Context(), p.ID, pg.User.ID, verdict, body); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.review", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(p.Number, 10), verdict)
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request_review", map[string]any{"action": "submitted", "number": p.Number,
		"title": p.Title, "state": verdict, "body": body, "user": pg.User.Name})
	s.setFlash(w, "berjaya", pg.T("ui.pull.reviewed"))
	http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
}

func (s *Server) uiPullState(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, _, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	_, manage := s.pullCan(pg, rc)
	author := pg.User != nil && pg.User.ID == p.AuthorID && !rc.Repo.Archived
	if !(manage || author) || p.Merged() || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	closed := r.PostFormValue("keadaan") == "closed"
	if err := s.Store.SetPullState(r.Context(), p.ID, closed); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, err)
		return
	}
	target := rc.Repo.Owner + "/" + rc.Repo.Name + "#" + strconv.FormatInt(p.Number, 10)
	if closed {
		_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.close", target, "")
		s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request", pullEvent(p, "", "closed"))
		s.setFlash(w, "berjaya", pg.T("ui.pull.closed_flash", p.Number))
	} else {
		_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.reopen", target, "")
		s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request", pullEvent(p, "", "reopened"))
		s.setFlash(w, "berjaya", pg.T("ui.pull.reopened_flash", p.Number))
	}
	http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
}

func (s *Server) uiPullEdit(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	_, manage := s.pullCan(pg, rc)
	author := pg.User != nil && pg.User.ID == p.AuthorID && !rc.Repo.Archived
	if !(manage || author) || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.bodyLimit(r))+4096)
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	if title == "" || len([]rune(title)) > pullTitleMax || len(body) > s.bodyLimit(r) {
		s.renderPull(w, r, pg, g, rc, p, pullData{Error: pg.T("ui.issue.bad_title", pullTitleMax)}, http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdatePull(r.Context(), p.ID, title, body); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.edit", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(p.Number, 10), title)
	p.Title, p.Body = title, body
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request", pullEvent(p, "", "edited"))
	s.setFlash(w, "berjaya", pg.T("ui.pull.edited"))
	http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
}

// uiPullMerge merges in the chosen style (D88: merge commit, squash or rebase, as the repository allows) and moves the
// base branch (compare-and-swap on the base id read in the same request). The person merging is the committer; a
// squash commit is authored by whoever opened the pull request, replayed commits keep their own authors.

// commitEmail is the address written into commits for u: their email, or name@forge-host when they set none.
func (s *Server) commitEmail(u store.User) string {
	if u.Email != "" {
		return u.Email
	}
	return u.Name + "@" + strings.TrimPrefix(strings.TrimPrefix(s.UI.HTTPHost, "https://"), "http://")
}

func (s *Server) uiPullMerge(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, g, p, ok := s.openPull(w, r, pg)
	if !ok {
		return
	}
	if pg.User == nil || !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	d := pullData{Pull: p}
	if err := s.pullState(r, pg, g, rc, &d); err != nil {
		s.fail(w, r, err)
		return
	}
	if !d.CanMerge {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	baseID, err := g.Resolve(r.Context(), p.BaseBranch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	style := r.PostFormValue("cara")
	if style == "" {
		style = rc.Repo.Styles()[0]
	}
	if !rc.Repo.Allows(style) {
		s.setFlash(w, "ralat", pg.T("ui.pull.style_off"))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	}
	me := gitread.Ident{Name: pg.User.Name, Email: s.commitEmail(*pg.User)}
	num := " (#" + strconv.FormatInt(p.Number, 10) + ")"
	var commit string
	var paths []string
	switch style {
	case "squash":
		author := me
		if p.ExtURL == "" && p.AuthorID != pg.User.ID {
			if u, err := s.Store.UserByID(r.Context(), p.AuthorID); err == nil {
				author = gitread.Ident{Name: u.Name, Email: s.commitEmail(u)}
			}
		}
		// every squashed commit keeps its whole message, oldest first, so "Fixes #n" in a commit body still closes the issue
		// once the squash lands (D87/D94), as it would have with a merge commit
		msg := p.Title + num + "\n"
		if cs, err := g.LogRange(r.Context(), baseID, d.HeadRef, 100); err == nil && len(cs) > 0 {
			for k := len(cs) - 1; k >= 0; k-- {
				msg += "\n* " + cs[k].Subject + "\n"
				if b := strings.TrimSpace(cs[k].Body); b != "" {
					msg += "\n" + b + "\n"
				}
			}
		}
		commit, err = g.Squash(r.Context(), p.BaseBranch, d.HeadRef, baseID, author, me, msg)
	case "rebase":
		commit, paths, err = g.Rebase(r.Context(), p.BaseBranch, d.HeadRef, baseID, me, int(s.settingInt(r.Context(), "pull.rebase_max_commits", 250)))
	default:
		msg := "Merge " + d.HeadLabel + " into " + p.BaseBranch + num + "\n\n" + p.Title + "\n"
		commit, err = g.Merge(r.Context(), p.BaseBranch, d.HeadRef, baseID, me.Name, me.Email, msg)
	}
	switch {
	case style == "rebase" && errors.Is(err, gitread.ErrConflict):
		s.setFlash(w, "ralat", pg.T("ui.pull.rebase_conflict", strings.Join(paths, ", ")))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	case errors.Is(err, gitread.ErrNotLinear):
		s.setFlash(w, "ralat", pg.T("ui.pull.rebase_merges"))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	case errors.Is(err, gitread.ErrTooMany):
		s.setFlash(w, "ralat", pg.T("ui.pull.rebase_many", s.settingInt(r.Context(), "pull.rebase_max_commits", 250)))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	case errors.Is(err, gitread.ErrConflict), errors.Is(err, gitread.ErrStale):
		s.setFlash(w, "ralat", pg.T("ui.pull.merge_failed"))
		http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	if s.LFS != nil && p.HeadRepoID > 0 && p.HeadRepoID != rc.Repo.ID { // merged from a fork: its LFS objects come along (D82)
		if _, err := s.LFS.LinkAll(p.HeadRepoID, *s.LFS, rc.Repo.ID); err != nil {
			s.Log.Error("merge: lfs objects", "err", err)
		}
	}
	if err := s.Store.SetPullMerged(r.Context(), p.ID, pg.User.ID, commit); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "pr.merge", rc.Repo.Owner+"/"+rc.Repo.Name+"#"+strconv.FormatInt(p.Number, 10), d.HeadLabel+" → "+p.BaseBranch+" "+commit[:7]+" "+style)
	_ = s.Store.Touch(r.Context(), rc.Repo.ID)
	p.MergeCommit = commit
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "pull_request", pullEvent(p, d.HeadLabel, "merged"))
	s.emit(r.Context(), rc.Repo.ID, pg.User.ID, "push", map[string]any{"ref": "refs/heads/" + p.BaseBranch, "before": baseID, "after": commit, "via": "merge"})
	s.setFlash(w, "berjaya", pg.T("ui.pull.merged_flash", p.Number, commit[:7]))
	http.Redirect(w, r, s.pullPath(rc, p), http.StatusSeeOther)
}
