package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"codr.my/codr/internal/names"
	"codr.my/codr/internal/store"
)

// Organisations and teams (D74). Every change here is an owner action on /org/{name}; who can READ or PUSH is
// decided only by store.AccessFor, which these pages merely feed.

type orgHeader struct {
	Name      string
	Members   int
	Teams     int
	CanManage bool
}

type orgPage struct {
	Org     store.Org
	Members []store.OrgMember
	Teams   []store.Team
	Repos   []store.Repo
	Error   string
	Name    string // create form
}

func (s *Server) uiOrgNew(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/org/baharu", http.StatusSeeOther)
		return
	}
	if allow, _ := s.Store.SettingBool(r.Context(), "org.allow_create"); !allow && !p.User.IsAdmin {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	d := orgPage{}
	code := http.StatusOK
	if r.Method == http.MethodPost {
		if !s.checkCSRF(r) {
			s.errPage(w, r, http.StatusForbidden)
			return
		}
		d.Name = strings.TrimSpace(r.PostFormValue("name"))
		switch err := names.User(d.Name); {
		case err != nil:
			d.Error, code = p.T("ui.org.bad_name"), http.StatusBadRequest
		default:
			o, err := s.Store.CreateOrg(r.Context(), d.Name, p.User.ID)
			if errors.Is(err, store.ErrExists) {
				d.Error, code = p.T("ui.org.taken", d.Name), http.StatusConflict
				break
			}
			if err != nil {
				s.fail(w, r, err)
				return
			}
			_ = s.Store.Audit(r.Context(), p.User.ID, "org.create", o.Name, "")
			s.setFlash(w, "berjaya", p.T("ui.org.created", o.Name))
			http.Redirect(w, r, "/org/"+o.Name, http.StatusSeeOther)
			return
		}
	}
	p.Title = p.T("ui.org.new_title") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, code, "org_new.html", p)
}

// orgOwner loads /org/{name} for one of its owners (site admins included).
func (s *Server) orgOwner(w http.ResponseWriter, r *http.Request) (page, store.Org, bool) {
	p := s.newPage(w, r, "")
	org, err := s.Store.OrgByName(r.Context(), r.PathValue("name"))
	if err != nil {
		s.errPage(w, r, http.StatusNotFound)
		return p, org, false
	}
	if p.User == nil {
		http.Redirect(w, r, "/login?next=/org/"+org.Name, http.StatusSeeOther)
		return p, org, false
	}
	role, err := s.Store.OrgRole(r.Context(), org.ID, p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return p, org, false
	}
	if role != "owner" && !p.User.IsAdmin {
		s.errPage(w, r, http.StatusNotFound) // members and strangers learn nothing about the management page
		return p, org, false
	}
	if r.Method == http.MethodPost && !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return p, org, false
	}
	return p, org, true
}

func (s *Server) orgRender(w http.ResponseWriter, r *http.Request, p page, org store.Org, msg string, code int) {
	ctx := r.Context()
	d := orgPage{Org: org, Error: msg}
	var err error
	if d.Members, err = s.Store.OrgMembers(ctx, org.ID); err == nil {
		d.Teams, err = s.Store.Teams(ctx, org.ID)
	}
	if err == nil {
		d.Repos, err = s.Store.Repos(ctx, org.Name)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.takeFlash(w, r, &p)
	p.Title = org.Name + " · " + p.T("ui.org.manage") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, code, "org.html", p)
}

func (s *Server) uiOrg(w http.ResponseWriter, r *http.Request) {
	p, org, ok := s.orgOwner(w, r)
	if ok {
		s.orgRender(w, r, p, org, "", http.StatusOK)
	}
}

// uiOrgAction handles every owner form on /org/{name}: one switch, one audit line each.
func (s *Server) uiOrgAction(w http.ResponseWriter, r *http.Request) {
	p, org, ok := s.orgOwner(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	id := func(k string) int64 { n, _ := strconv.ParseInt(f(k), 10, 64); return n }
	user := func() (store.User, error) {
		u, err := s.Store.UserByName(ctx, f("user"))
		if err == nil && u.IsOrg {
			err = store.ErrIsOrg
		}
		return u, err
	}
	var err error
	action := r.PathValue("action")
	detail := ""
	switch action {
	case "ahli":
		var u store.User
		if u, err = user(); err == nil {
			role := f("role")
			if role != "owner" && role != "member" {
				err = store.ErrNotFound
			} else {
				err = s.Store.SetOrgMember(ctx, org.ID, u.ID, role)
				detail = u.Name + " " + role
			}
		}
	case "buang":
		var u store.User
		if u, err = user(); err == nil {
			err = s.Store.RemoveOrgMember(ctx, org.ID, u.ID)
			detail = u.Name
		}
	case "asas":
		role := f("role")
		if role != "none" && role != "read" && role != "write" {
			err = store.ErrNotFound
		} else {
			err = s.Store.SetOrgBaseRole(ctx, org.ID, role)
			detail = role
		}
	case "pasukan":
		name := f("name")
		if names.Repo(name) != nil {
			err = store.ErrNotFound
		} else {
			_, err = s.Store.CreateTeam(ctx, org.ID, name)
			detail = name
		}
	case "pasukan-padam":
		err = s.Store.DeleteTeam(ctx, org.ID, id("team"))
		detail = f("team")
	case "pasukan-ahli":
		var u store.User
		if u, err = user(); err == nil {
			if f("op") == "remove" {
				err = s.Store.RemoveTeamMember(ctx, org.ID, id("team"), u.ID)
			} else {
				err = s.Store.SetTeamMember(ctx, org.ID, id("team"), u.ID)
			}
			detail = f("team") + " " + u.Name + " " + f("op")
		}
	case "pasukan-repo":
		role := f("role")
		switch {
		case f("op") == "remove":
			err = s.Store.RemoveTeamRepo(ctx, org.ID, id("team"), id("repo"))
		case role == "read" || role == "write" || role == "admin":
			err = s.Store.SetTeamRepo(ctx, org.ID, id("team"), id("repo"), role)
		default:
			err = store.ErrNotFound
		}
		detail = f("team") + " repo " + f("repo") + " " + role + f("op")
	default:
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	if err != nil {
		msg := p.T("ui.org.bad_input")
		switch {
		case errors.Is(err, store.ErrLastOwner):
			msg = p.T("ui.org.last_owner")
		case errors.Is(err, store.ErrExists):
			msg = p.T("ui.org.team_taken")
		case errors.Is(err, store.ErrIsOrg):
			msg = p.T("ui.org.not_person")
		}
		s.orgRender(w, r, p, org, msg, http.StatusBadRequest)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "org."+action, org.Name, detail)
	s.setFlash(w, "berjaya", p.T("ui.org.saved"))
	http.Redirect(w, r, "/org/"+org.Name, http.StatusSeeOther)
}
