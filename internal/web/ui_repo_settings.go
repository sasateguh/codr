package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"codr.my/codr/internal/store"
)

// /{owner}/{repo}/tetapan (D88): the repository settings a repository administrator changes without the CLI: the
// description and which pull request merge styles are offered (and which one is preselected).

const repoDescMax = 255 // the same limit as the new-repository form and `codr admin repo describe`

type repoSettingsData struct {
	repoCtx
	Styles      []string        // every style, canonical order
	On          map[string]bool // allowed now
	First       string          // preselected
	Description string
	Error       string
	Rules       []store.Protection // protected branches (D89)
	DefaultOn   bool               // repo.protect_default_branch protects the default branch without a rule
	Pattern     string             // the add form, kept after an error
}

func (s *Server) openRepoSettings(w http.ResponseWriter, r *http.Request, pg page) (repoCtx, bool) {
	rc, _, ok := s.openRepo(w, r, pg, "")
	if !ok {
		return rc, false
	}
	if pg.User == nil || rc.Access < store.AccessAdmin {
		s.errPage(w, r, http.StatusNotFound)
		return rc, false
	}
	rc.Tab = "settings"
	return rc, true
}

func (s *Server) renderRepoSettings(w http.ResponseWriter, r *http.Request, pg page, d repoSettingsData, code int) {
	d.Styles = store.MergeStyleNames
	var err error
	if d.Rules, err = s.Store.Protections(r.Context(), d.Repo.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.DefaultOn, _ = s.Store.SettingBool(r.Context(), "repo.protect_default_branch")
	pg.Title = pg.T("ui.rset.title") + " · " + d.Repo.Name + " · " + pg.SiteName
	s.render(w, r, code, "repo_settings.html", pg.with(d))
}

// currentSettings is the form filled with what the repository holds now.
func currentSettings(rc repoCtx) repoSettingsData {
	d := repoSettingsData{repoCtx: rc, On: map[string]bool{}, Description: rc.Repo.Description, First: rc.Repo.Styles()[0]}
	for _, x := range rc.Repo.Styles() {
		d.On[x] = true
	}
	return d
}

func (s *Server) uiRepoSettings(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openRepoSettings(w, r, pg)
	if !ok {
		return
	}
	s.takeFlash(w, r, &pg)
	s.renderRepoSettings(w, r, pg, currentSettings(rc), http.StatusOK)
}

func (s *Server) uiRepoSettingsSave(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openRepoSettings(w, r, pg)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	d := repoSettingsData{repoCtx: rc, On: map[string]bool{}, First: r.PostFormValue("utama"),
		Description: strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("description"), "\n", " "))}
	for _, x := range r.PostForm["cara"] {
		for _, ok := range store.MergeStyleNames {
			if x == ok {
				d.On[x] = true
			}
		}
	}
	switch {
	case utf8.RuneCountInString(d.Description) > repoDescMax:
		d.Error = pg.T("ui.rset.desc_long", repoDescMax)
	case len(d.On) == 0:
		d.Error = pg.T("ui.rset.need_style")
	case !d.On[d.First]:
		d.Error = pg.T("ui.rset.first_off")
	}
	if d.Error != "" {
		s.renderRepoSettings(w, r, pg, d, http.StatusBadRequest)
		return
	}
	styles := []string{d.First}
	for _, x := range store.MergeStyleNames {
		if d.On[x] && x != d.First {
			styles = append(styles, x)
		}
	}
	target := rc.Repo.Owner + "/" + rc.Repo.Name
	if d.Description != rc.Repo.Description {
		if err := s.Store.SetDescription(r.Context(), rc.Repo.ID, d.Description); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), pg.User.ID, "repo.describe", target, d.Description)
	}
	if joined := strings.Join(styles, ","); joined != strings.Join(rc.Repo.Styles(), ",") {
		if err := s.Store.SetMergeStyles(r.Context(), rc.Repo.ID, styles); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), pg.User.ID, "repo.merge_styles", target, joined)
	}
	s.setFlash(w, "berjaya", pg.T("ui.rset.saved"))
	http.Redirect(w, r, "/"+target+"/tetapan", http.StatusSeeOther)
}

// uiRepoProtect adds or updates one protected-branch rule (the same rows `codr admin protect` writes), or removes one.
func (s *Server) uiRepoProtect(w http.ResponseWriter, r *http.Request) {
	pg := s.newPage(w, r, "")
	rc, ok := s.openRepoSettings(w, r, pg)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	target := rc.Repo.Owner + "/" + rc.Repo.Name
	pattern := strings.TrimSpace(r.PostFormValue("corak"))
	if r.PathValue("tindakan") == "buang" {
		if err := s.Store.Unprotect(r.Context(), rc.Repo.ID, pattern); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), pg.User.ID, "protect.unset", target, pattern)
		s.setFlash(w, "berjaya", pg.T("ui.rset.rule_removed", pattern))
		http.Redirect(w, r, "/"+target+"/tetapan#lindung", http.StatusSeeOther)
		return
	}
	force, del, checks := r.PostFormValue("paksa") == "1", r.PostFormValue("padam") == "1", r.PostFormValue("semakan") == "1"
	if pattern == "" || utf8.RuneCountInString(pattern) > 100 || strings.ContainsAny(pattern, " \t\n") {
		d := currentSettings(rc)
		d.Pattern, d.Error = pattern, pg.T("ui.rset.rule_bad")
		s.renderRepoSettings(w, r, pg, d, http.StatusBadRequest)
		return
	}
	if err := s.Store.Protect(r.Context(), rc.Repo.ID, pattern, force, del, checks); err != nil {
		d := currentSettings(rc)
		d.Pattern, d.Error = pattern, pg.T("ui.rset.rule_bad")
		s.renderRepoSettings(w, r, pg, d, http.StatusBadRequest)
		return
	}
	_ = s.Store.Audit(r.Context(), pg.User.ID, "protect.set", target, fmt.Sprintf("%s force=%t delete=%t checks=%t", pattern, force, del, checks))
	s.setFlash(w, "berjaya", pg.T("ui.rset.rule_saved", pattern))
	http.Redirect(w, r, "/"+target+"/tetapan#lindung", http.StatusSeeOther)
}
