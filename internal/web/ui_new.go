package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/mirror"
	"codr.my/codr/internal/names"
	"codr.my/codr/internal/store"
)

type newData struct {
	Name, Desc, URL string
	Private         bool
	Error           string
	CanMirror       bool
	SSHHost         string
	HTTPHost        string
	User            string
	Owner           string   // who will own the new repo (D74)
	Owners          []string // the person, then organisations they own
}

// ownersFor lists who a person may create a repository for: themselves and the organisations they own.
func (s *Server) ownersFor(ctx context.Context, u store.User) []string {
	out := []string{u.Name}
	if orgs, err := s.Store.OrgsOf(ctx, u.ID); err == nil {
		for _, o := range orgs {
			if o.Role == "owner" {
				out = append(out, o.Name)
			}
		}
	}
	return out
}

// uiNewForm shows the create-repo form (mirror fields only when a syncer is configured).
func (s *Server) uiNewForm(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/baharu", http.StatusSeeOther)
		return
	}
	private, _ := s.Store.SettingBool(r.Context(), "repo.default_private")
	p.Title = p.T("ui.new.title") + " · " + p.SiteName
	owners := s.ownersFor(r.Context(), *p.User)
	owner := r.URL.Query().Get("pemilik")
	if owner == "" {
		owner = p.User.Name
	}
	p.Data = newData{Private: private, CanMirror: s.Mirror != nil, SSHHost: s.UI.SSHHost, HTTPHost: s.UI.HTTPHost, User: p.User.Name, Owner: owner, Owners: owners}
	s.render(w, r, http.StatusOK, "new.html", p)
}

// uiNewCreate creates the repo (and its mirror row) for the logged-in user.
func (s *Server) uiNewCreate(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/baharu", http.StatusSeeOther)
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	d := newData{Name: strings.TrimSpace(r.PostFormValue("name")), Desc: strings.TrimSpace(r.PostFormValue("desc")),
		URL: strings.TrimSpace(r.PostFormValue("url")), Private: r.PostFormValue("visibility") != "public",
		CanMirror: s.Mirror != nil, SSHHost: s.UI.SSHHost, HTTPHost: s.UI.HTTPHost, User: p.User.Name,
		Owner: strings.TrimSpace(r.PostFormValue("owner")), Owners: s.ownersFor(r.Context(), *p.User)}
	if d.Owner == "" {
		d.Owner = p.User.Name
	}
	ownerID := p.User.ID
	if d.Owner != p.User.Name { // an organisation: only its owners may create repositories in it (checked here, not by the form)
		org, err := s.Store.OrgByName(r.Context(), d.Owner)
		role := ""
		if err == nil {
			role, err = s.Store.OrgRole(r.Context(), org.ID, p.User.ID)
		}
		if err != nil || role != "owner" {
			s.errPage(w, r, http.StatusForbidden)
			return
		}
		ownerID = org.ID
	}
	token := strings.TrimSpace(r.PostFormValue("token"))
	p.Title = p.T("ui.new.title") + " · " + p.SiteName
	fail := func(code int, msg string) {
		d.Error = msg
		s.render(w, r, code, "new.html", p.with(d))
	}
	if err := names.Repo(d.Name); err != nil {
		fail(http.StatusBadRequest, p.T("ui.new.bad_name"))
		return
	}
	if len(d.Desc) > 255 {
		d.Desc = d.Desc[:255]
	}
	tokenEnc := ""
	if d.URL != "" {
		if s.Mirror == nil || mirror.ValidURL(d.URL, false) != nil {
			fail(http.StatusBadRequest, p.T("ui.new.bad_url"))
			return
		}
		if token != "" {
			if s.Mirror.Key == nil {
				fail(http.StatusBadRequest, p.T("ui.new.no_key"))
				return
			}
			var err error
			if tokenEnc, err = s.Mirror.Key.Seal(token); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	}
	if _, err := s.Store.RepoByPath(r.Context(), d.Owner, d.Name); err == nil {
		fail(http.StatusConflict, p.T("ui.new.exists", d.Name))
		return
	}
	branch, err := s.Store.Setting(r.Context(), "repo.default_branch")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	repo, err := s.Store.CreateRepo(r.Context(), ownerID, d.Name, d.Desc, d.Private, branch)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Disk.Create(r.Context(), repo.ID, branch); err != nil {
		_ = s.Store.DeleteRepo(r.Context(), repo.ID)
		s.fail(w, r, err)
		return
	}
	ref := d.Owner + "/" + d.Name
	if d.URL != "" {
		interval, _ := s.Store.SettingInt(r.Context(), "mirror.default_interval_minutes")
		if interval < 1 {
			interval = 60
		}
		if err := s.Store.SetMirror(r.Context(), repo.ID, d.URL, tokenEnc, interval); err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), p.User.ID, "repo.import", ref, d.URL)
		go func(id int64) { // first sync may take minutes: the page must not wait for it
			ctx, cancel := context.WithTimeout(context.Background(), s.Mirror.Timeout+time.Minute)
			defer cancel()
			_ = s.Mirror.Sync(ctx, id)
		}(repo.ID)
		s.setFlash(w, "berjaya", p.T("ui.new.created_mirror", ref))
	} else {
		_ = s.Store.Audit(r.Context(), p.User.ID, "repo.create", ref, "web")
		s.setFlash(w, "berjaya", p.T("ui.new.created", ref))
	}
	http.Redirect(w, r, "/"+ref, http.StatusSeeOther)
}

type auditData struct {
	Rows  []store.AuditRow
	Page  int
	Pages int
}

func (s *Server) uiAudit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	size, err := s.Store.SettingInt(r.Context(), "ui.page_size")
	if err != nil || size < 1 {
		size = 50
	}
	pg, _ := strconv.Atoi(r.URL.Query().Get("muka"))
	if pg < 1 {
		pg = 1
	}
	rows, total, err := s.Store.AuditRows(r.Context(), int(size), (pg-1)*int(size))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p.Title = p.T("ui.audit.title") + " · " + p.SiteName
	p.Data = auditData{Rows: rows, Page: pg, Pages: (total + int(size) - 1) / int(size)}
	s.render(w, r, http.StatusOK, "audit.html", p)
}

// mirrorInfo is what the repo page shows for a mirror.
type mirrorInfo struct {
	URL     string
	OKAt    time.Time
	Error   string
	Pending bool
}

func (s *Server) mirrorInfo(ctx context.Context, repo store.Repo) *mirrorInfo {
	if !repo.Mirror {
		return nil
	}
	m, err := s.Store.MirrorOf(ctx, repo.ID)
	if err != nil {
		return nil
	}
	mi := &mirrorInfo{URL: m.URL, Error: m.LastError, Pending: m.LastOKAt == ""}
	if m.LastOKAt != "" {
		mi.OKAt, _ = time.Parse("2006-01-02T15:04:05.000Z", m.LastOKAt)
	}
	return mi
}
