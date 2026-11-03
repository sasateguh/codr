package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/store"
)

type loginData struct {
	User  string
	Error string
	Next  string
	OIDC  string // the provider's button label when sign-in through it is on (D91); "" = no button
}

// oidcName is the label of the provider button, or "" when sign-in through a provider is off.
func (s *Server) oidcName(r *http.Request) string {
	if c, ok := s.oidcReady(r.Context()); ok {
		return c.Name
	}
	return ""
}

func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\r\n") {
		return "/"
	}
	return next
}

func (s *Server) uiLoginForm(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	if p.User != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	p.Title = p.T("ui.login.title") + " · " + p.SiteName
	p.Data = loginData{Next: safeNext(r.URL.Query().Get("next")), OIDC: s.oidcName(r)}
	s.render(w, r, http.StatusOK, "login.html", p)
}

func (s *Server) uiLogin(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	p.Title = p.T("ui.login.title") + " · " + p.SiteName
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("user")))
	next := safeNext(r.PostFormValue("next"))
	d := loginData{User: name, Next: next, OIDC: s.oidcName(r)}
	ip := s.clientIP(r)
	if !s.checkCSRF(r) {
		d.Error = p.T("ui.login.csrf")
		s.render(w, r, http.StatusBadRequest, "login.html", p.with(d))
		return
	}
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		d.Error = p.T("http.too_many")
		s.render(w, r, http.StatusTooManyRequests, "login.html", p.with(d))
		return
	}
	u, err := s.Store.UserByName(r.Context(), name)
	ok := err == nil && !u.Disabled && !u.IsOrg // an organisation never signs in (D74)
	var hash string
	if ok {
		hash, err = s.Store.PasswordHash(r.Context(), u.ID)
		ok = err == nil && auth.Verify(r.PostFormValue("password"), hash) == nil
	} else {
		// constant-ish time: hash anyway so a missing user costs the same as a wrong password
		_ = auth.Verify(r.PostFormValue("password"), s.UI.dummyHash)
	}
	if !ok {
		if s.Limiter != nil {
			s.Limiter.Fail(ip)
		}
		s.Log.Warn("login failed", "user", name, "ip", ip)
		d.Error = p.T("ui.login.failed")
		s.render(w, r, http.StatusUnauthorized, "login.html", p.with(d))
		return
	}
	if auth.NeedsRehash(hash, s.UI.Argon) {
		if h, err := auth.Hash(r.PostFormValue("password"), s.UI.Argon); err == nil {
			_ = s.Store.SetPasswordHash(r.Context(), u.ID, h)
		}
	}
	s.firstFactorDone(w, r, u, next, ip, ip)
}

// firstFactorDone is the one place a first factor (a password, or the provider in D91) becomes a session: with 2FA on,
// a challenge first (D80), never a session. detail goes into the audit row.
func (s *Server) firstFactorDone(w http.ResponseWriter, r *http.Request, u store.User, next, ip, detail string) {
	if tf, err := s.Store.TwoFactor(r.Context(), u.ID); err == nil && tf.On != "" {
		// the first factor was right, but nothing is signed in until the code is (D80)
		if err := s.startChallenge(w, r, u, next, ip); err != nil {
			s.fail(w, r, err)
		}
		return
	}
	token, _, err := s.Store.CreateSession(r.Context(), u.ID, s.UI.SessionTTL, r.UserAgent(), ip)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: token, Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(s.UI.SessionTTL / time.Second)})
	_ = s.Store.Audit(r.Context(), u.ID, "user.login", u.Name, detail)
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (p page) with(d any) page { p.Data = d; return p }

func (s *Server) uiLogout(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	if c, err := r.Cookie(cookieSession); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: "", Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// uiLang sets the language cookie from the footer form.
func (s *Server) uiLang(w http.ResponseWriter, r *http.Request) {
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	l := i18n.Normalize(r.PostFormValue("lang"), "")
	if l == "" {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieLang, Value: l, Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 365})
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

// requireAdmin returns the page or renders the refusal (login page for anonymous, 403 otherwise).
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (page, bool) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return p, false
	}
	if !p.User.IsAdmin {
		s.errPage(w, r, http.StatusForbidden)
		return p, false
	}
	return p, true
}

func (s *Server) uiSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	rows, err := s.Store.Settings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.takeFlash(w, r, &p)
	p.Title = p.T("ui.settings.title") + " · " + p.SiteName
	p.Data = rows
	s.render(w, r, http.StatusOK, "settings.html", p)
}

func (s *Server) uiSettingsSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	key, value := r.PostFormValue("key"), strings.TrimSpace(r.PostFormValue("value"))
	typ, err := s.Store.SetSettingChecked(r.Context(), key, value, p.User.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, "ralat", p.T("ui.settings.unknown", key))
	case errors.Is(err, store.ErrBadValue):
		s.setFlash(w, "ralat", p.T("ui.settings.bad_value", key, typ))
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		_ = s.Store.Audit(r.Context(), p.User.ID, "setting.set", key, value)
		s.setFlash(w, "berjaya", p.T("ui.settings.saved", key))
	}
	http.Redirect(w, r, "/admin/tetapan", http.StatusSeeOther)
}
