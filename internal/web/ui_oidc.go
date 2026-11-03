package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"codr.my/codr/internal/oidc"
	"codr.my/codr/internal/store"
)

// Sign-in through an OpenID Connect provider (D91). Off until an administrator fills /admin/oidc and switches it on.
//   - /login/oidc starts a flow: state (returned by the provider), nonce (must come back inside the ID token), PKCE
//     verifier, and a cookie that binds the flow to this browser (a callback URL sent to someone else signs nobody in)
//   - /login/oidc/balik takes the flow once, exchanges the code, verifies the ID token, then finds the account:
//     the provider subject already linked, else the ONE active account whose email equals the provider's VERIFIED
//     email (and links it). No account is ever created. The rest is firstFactorDone: 2FA still applies.

const cookieOIDC = "codr_oidc"

func (s *Server) oidcClient(ctx context.Context) oidc.Client {
	secs := s.settingInt(ctx, "auth.oidc_timeout_seconds", 15)
	return oidc.Client{HTTP: &http.Client{Timeout: time.Duration(secs) * time.Second}, Insecure: s.OIDCInsecure}
}

func (s *Server) oidcRedirect() string {
	return strings.TrimRight(s.UI.HTTPHost, "/") + "/login/oidc/balik"
}

// oidcReady returns the configuration when sign-in through the provider is on and complete.
func (s *Server) oidcReady(ctx context.Context) (store.OIDCConfig, bool) {
	c, err := s.Store.OIDCConfig(ctx)
	if err != nil || !c.Ready() || s.Mirror == nil || s.Mirror.Key == nil {
		return c, false
	}
	return c, true
}

func (s *Server) oidcFail(w http.ResponseWriter, r *http.Request, why string, err error) {
	if err != nil {
		s.Log.Warn("oidc sign-in refused", "why", why, "err", err, "ip", s.clientIP(r))
	}
	if s.Limiter != nil {
		s.Limiter.Fail(s.clientIP(r))
	}
	p := s.newPage(w, r, "")
	p.Title = p.T("ui.login.title") + " · " + p.SiteName
	d := loginData{Next: "/", Error: p.T("ui.oidc.fail." + why), OIDC: s.oidcName(r)}
	s.render(w, r, http.StatusUnauthorized, "login.html", p.with(d))
}

func (s *Server) uiOIDCStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.oidcReady(ctx)
	if !ok {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	if s.Limiter != nil && s.Limiter.Blocked(s.clientIP(r)) {
		s.text(w, r, http.StatusTooManyRequests, "http.too_many")
		return
	}
	d, err := s.oidcClient(ctx).Discover(ctx, c.Issuer)
	if err != nil {
		s.oidcFail(w, r, "provider", err)
		return
	}
	f := store.OIDCFlow{State: oidc.Random(24), Nonce: oidc.Random(24), Verifier: oidc.Random(48), Next: safeNext(r.URL.Query().Get("next"))}
	browser := oidc.Random(24)
	ttl := time.Duration(s.settingInt(ctx, "auth.oidc_flow_minutes", 10)) * time.Minute
	if err := s.Store.StartOIDCFlow(ctx, f, browser, ttl); err != nil {
		s.fail(w, r, err)
		return
	}
	// Lax, not Strict: the provider sends the browser back with a top-level GET from another site
	http.SetCookie(w, &http.Cookie{Name: cookieOIDC, Value: browser, Path: "/login/oidc", HttpOnly: true, Secure: s.UI.SecureCookies,
		SameSite: http.SameSiteLaxMode, MaxAge: int(ttl / time.Second)})
	http.Redirect(w, r, oidc.AuthURL(d, c.ClientID, s.oidcRedirect(), c.Scopes, f.State, f.Nonce, f.Verifier), http.StatusFound)
}

func (s *Server) uiOIDCReturn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.oidcReady(ctx)
	if !ok {
		s.errPage(w, r, http.StatusNotFound)
		return
	}
	ip := s.clientIP(r)
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		s.text(w, r, http.StatusTooManyRequests, "http.too_many")
		return
	}
	q := r.URL.Query()
	browser := ""
	if ck, err := r.Cookie(cookieOIDC); err == nil {
		browser = ck.Value
	}
	http.SetCookie(w, &http.Cookie{Name: cookieOIDC, Value: "", Path: "/login/oidc", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	ttl := time.Duration(s.settingInt(ctx, "auth.oidc_flow_minutes", 10)) * time.Minute
	f, err := s.Store.TakeOIDCFlow(ctx, q.Get("state"), browser, ttl)
	if err != nil {
		s.oidcFail(w, r, "flow", err)
		return
	}
	if e := q.Get("error"); e != "" || q.Get("code") == "" {
		s.oidcFail(w, r, "provider", errors.New("provider answered: "+e))
		return
	}
	secret, err := s.Mirror.Key.Open(c.SecretSealed)
	if err != nil {
		s.oidcFail(w, r, "provider", err)
		return
	}
	cl := s.oidcClient(ctx)
	d, err := cl.Discover(ctx, c.Issuer)
	if err != nil {
		s.oidcFail(w, r, "provider", err)
		return
	}
	raw, err := cl.Exchange(ctx, d, c.ClientID, secret, s.oidcRedirect(), q.Get("code"), f.Verifier)
	if err != nil {
		s.oidcFail(w, r, "provider", err)
		return
	}
	claims, err := cl.Verify(ctx, d, raw, c.ClientID, f.Nonce, time.Now(), time.Minute)
	if err != nil {
		s.oidcFail(w, r, "token", err)
		return
	}
	issuer := strings.TrimRight(c.Issuer, "/")
	u, err := s.Store.OIDCUser(ctx, issuer, claims.Subject)
	if errors.Is(err, store.ErrNotFound) {
		if !claims.EmailVerified || claims.Email == "" {
			s.oidcFail(w, r, "unverified", errors.New("email not verified: "+claims.Email))
			return
		}
		if u, err = s.Store.UserByEmail(ctx, claims.Email); err != nil {
			s.oidcFail(w, r, "no_account", err)
			return
		}
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	if u.Disabled || u.IsOrg {
		s.oidcFail(w, r, "no_account", errors.New("account disabled or an organisation"))
		return
	}
	if err := s.Store.LinkOIDC(ctx, issuer, claims.Subject, u.ID, claims.Email); err != nil {
		s.fail(w, r, err)
		return
	}
	s.firstFactorDone(w, r, u, f.Next, ip, "oidc "+hostOf(issuer)+" "+ip)
}

func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}
