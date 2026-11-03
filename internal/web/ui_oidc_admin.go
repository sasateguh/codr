package web

import (
	"net/http"
	"strings"

	"codr.my/codr/internal/store"
)

// /admin/oidc (D91): the provider, sealed client secret, switch and a discovery test; /akaun lists and removes links.

type oidcAdminData struct {
	Config    store.OIDCConfig
	HasSecret bool
	NoKey     bool
	Redirect  string // the redirect URI to register at the provider
	Error     string
}

func (s *Server) oidcAdminPage(w http.ResponseWriter, r *http.Request, p page, d oidcAdminData, code int) {
	cfg, err := s.Store.OIDCConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Error == "" {
		d.Config = cfg
	}
	d.HasSecret = cfg.SecretSealed != ""
	d.Config.SecretSealed = ""
	d.NoKey = s.Mirror == nil || s.Mirror.Key == nil
	d.Redirect = s.oidcRedirect()
	p.Title = p.T("ui.oidc.title") + " · " + p.SiteName
	p.Data = d
	s.render(w, r, code, "oidc.html", p)
}

func (s *Server) uiOIDCAdmin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	s.takeFlash(w, r, &p)
	s.oidcAdminPage(w, r, p, oidcAdminData{}, http.StatusOK)
}

func (s *Server) uiOIDCAdminSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) || s.Mirror == nil || s.Mirror.Key == nil {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	c, err := s.Store.OIDCConfig(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	c.Name, c.Issuer, c.ClientID, c.Scopes = f("name"), strings.TrimRight(f("issuer"), "/"), f("client_id"), strings.Join(strings.Fields(f("scopes")), " ")
	c.Enabled = r.PostFormValue("enabled") == "1"
	if c.Scopes == "" {
		c.Scopes = "openid email profile"
	}
	if sec := f("secret"); sec != "" {
		if c.SecretSealed, err = s.Mirror.Key.Seal(sec); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	bad := ""
	switch {
	case len(c.Name) > 60 || strings.ContainsAny(c.Name, "<>\n"):
		bad = "ui.oidc.bad_name"
	case c.Issuer != "" && !s.OIDCInsecure && !strings.HasPrefix(c.Issuer, "https://"):
		bad = "ui.oidc.bad_issuer"
	case len(c.ClientID) > 200 || strings.ContainsAny(c.ClientID, " \n\t"):
		bad = "ui.oidc.bad_client"
	case !strings.Contains(" "+c.Scopes+" ", " openid ") || !strings.Contains(" "+c.Scopes+" ", " email "):
		bad = "ui.oidc.bad_scopes"
	case c.Enabled && !c.Ready():
		bad = "ui.oidc.not_ready"
	}
	if bad != "" {
		c.SecretSealed = ""
		s.oidcAdminPage(w, r, p, oidcAdminData{Config: c, Error: p.T(bad)}, http.StatusBadRequest)
		return
	}
	if err := s.Store.SetOIDCConfig(ctx, c); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "oidc.config", c.Issuer, "enabled="+map[bool]string{true: "true", false: "false"}[c.Enabled]+" client="+c.ClientID)
	s.setFlash(w, "berjaya", p.T("ui.oidc.saved"))
	http.Redirect(w, r, "/admin/oidc", http.StatusSeeOther)
}

// uiOIDCAdminTest runs discovery against the saved issuer and says what it found.
func (s *Server) uiOIDCAdminTest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	c, err := s.Store.OIDCConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.oidcClient(r.Context()).Discover(r.Context(), c.Issuer)
	if err != nil {
		s.setFlash(w, "ralat", p.T("ui.oidc.test_failed", err.Error()))
	} else {
		s.setFlash(w, "berjaya", p.T("ui.oidc.test_ok", hostOf(d.AuthorizationEndpoint)))
	}
	http.Redirect(w, r, "/admin/oidc", http.StatusSeeOther)
}

// uiOIDCUnlink removes one provider identity from the signed-in person's account.
func (s *Server) uiOIDCUnlink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	issuer, subject := r.PostFormValue("issuer"), r.PostFormValue("subject")
	if err := s.Store.UnlinkOIDC(r.Context(), p.User.ID, issuer, subject); err != nil {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "user.oidc_unlink", p.User.Name, hostOf(issuer))
	s.setFlash(w, "berjaya", p.T("ui.oidc.unlinked"))
	http.Redirect(w, r, "/akaun#oidc", http.StatusSeeOther)
}
