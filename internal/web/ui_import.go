package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"codr.my/codr/internal/ghimport"
)

// uiImportGitHub brings the issues, pull requests and comments of a github.com mirror in through the REST API,
// with the mirror's own sealed token (D72). Admins only; safe to press again (it updates, never duplicates).
func (s *Server) uiImportGitHub(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	rc, _, ok := s.openRepo(w, r, p, "")
	if !ok {
		return
	}
	if p.User == nil || !p.User.IsAdmin || !s.checkCSRF(r) || s.Mirror == nil {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	back := "/" + rc.Repo.Owner + "/" + rc.Repo.Name
	m, err := s.Store.MirrorOf(r.Context(), rc.Repo.ID)
	owner, repo := ghimport.ParseGitHubURL(m.URL)
	if err != nil || owner == "" {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	token := ""
	if m.TokenEnc != "" {
		if s.Mirror.Key == nil {
			s.errPage(w, r, http.StatusForbidden)
			return
		}
		if token, err = s.Mirror.Key.Open(m.TokenEnc); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 80*time.Second) // below Cloudflare's 100 s
	defer cancel()
	res, err := ghimport.Import(ctx, s.Store, rc.Repo.ID, p.User.ID, ghimport.API{BaseURL: s.GitHubAPI, Owner: owner, Repo: repo, Token: token})
	if err != nil {
		s.setFlash(w, "ralat", p.T("ui.import.failed", err.Error()))
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "github.import", rc.Repo.Owner+"/"+rc.Repo.Name,
		strconv.Itoa(res.Issues)+" issues, "+strconv.Itoa(res.Pulls)+" pulls, "+strconv.Itoa(res.Comments)+" comments new")
	s.setFlash(w, "berjaya", p.T("ui.import.done", res.Issues, res.Pulls, res.Comments, res.Updated))
	http.Redirect(w, r, back, http.StatusSeeOther)
}
