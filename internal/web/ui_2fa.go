package web

import (
	"context"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// Two-step sign-in (D80). Set up at /akaun/2fa with a QR code (or the secret typed by hand), confirmed with a code,
// then every web sign-in asks for a code after the password. Git is not affected: HTTPS takes tokens and SSH keys,
// never the password. Recovery codes (shown once) get a person in when the phone is gone; an admin can switch 2FA
// off from the server (`codr admin user 2fa-off NAME`) when those are gone too.

const cookieChallenge = "codr_2fa"

type twoFAData struct {
	Secret    string        // grouped for typing
	QR        template.HTML // inline SVG built by qrSVG from our own data, never from input
	URI       string
	Codes     []string // recovery codes, shown once
	Error     string
	On        bool
	Unused    int
	NoKey     bool
	Purpose   string // "setup" | "codes"
	PageTitle string
}

func (s *Server) settingInt(ctx context.Context, key string, def int64) int64 {
	v, err := s.Store.SettingInt(ctx, key)
	if err != nil || v < 0 {
		return def
	}
	return v
}

func groupSecret(sec string) string {
	var b strings.Builder
	for i, c := range sec {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// recoverySet makes a new set of recovery codes: the plain codes to show and the hashes to store.
func (s *Server) recoverySet(ctx context.Context) ([]string, []string, error) {
	n := int(s.settingInt(ctx, "auth.recovery_codes", 10))
	if n < 1 {
		n = 10
	}
	var plain, hashes []string
	for i := 0; i < n; i++ {
		c, err := auth.NewRecoveryCode()
		if err != nil {
			return nil, nil, err
		}
		plain = append(plain, c)
		hashes = append(hashes, store.RecoveryHash(auth.NormalizeRecovery(c)))
	}
	return plain, hashes, nil
}

func (s *Server) render2FA(w http.ResponseWriter, r *http.Request, p page, d twoFAData, code int) {
	p.Title = p.T("ui.2fa.title") + " · " + p.SiteName
	s.render(w, r, code, "twofa.html", p.with(d))
}

// ui2FASetup shows the QR code of a pending secret (made on first visit, kept across reloads until confirmed).
func (s *Server) ui2FASetup(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tf, err := s.Store.TwoFactor(ctx, p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if tf.On != "" {
		http.Redirect(w, r, "/akaun#dualangkah", http.StatusSeeOther)
		return
	}
	if s.Mirror == nil || s.Mirror.Key == nil {
		s.render2FA(w, r, p, twoFAData{NoKey: true, Purpose: "setup"}, http.StatusOK)
		return
	}
	var sec string
	if tf.Sealed != "" {
		sec, err = s.Mirror.Key.Open(tf.Sealed)
	}
	if tf.Sealed == "" || err != nil {
		if sec, err = auth.NewTOTPSecret(); err != nil {
			s.fail(w, r, err)
			return
		}
		sealed, err := s.Mirror.Key.Seal(sec)
		if err == nil {
			err = s.Store.SetTOTPPending(ctx, p.User.ID, sealed)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.render2FA(w, r, p, s.setupData(p, sec, ""), http.StatusOK)
}

func (s *Server) setupData(p page, sec, errMsg string) twoFAData {
	uri := auth.TOTPURI(p.SiteName, p.User.Name, sec)
	return twoFAData{Secret: groupSecret(sec), URI: uri, QR: qrSVG(uri), Error: errMsg, Purpose: "setup"}
}

// ui2FAConfirm switches 2FA on when the code from the app matches the pending secret.
func (s *Server) ui2FAConfirm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) || s.Mirror == nil || s.Mirror.Key == nil {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	tf, err := s.Store.TwoFactor(ctx, p.User.ID)
	if err != nil || tf.On != "" || tf.Sealed == "" {
		http.Redirect(w, r, "/akaun/2fa", http.StatusSeeOther)
		return
	}
	sec, err := s.Mirror.Key.Open(tf.Sealed)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	step, good := auth.VerifyTOTP(sec, r.PostFormValue("code"), time.Now(), int(s.settingInt(ctx, "auth.totp_skew_steps", 1)), 0)
	if !good {
		s.render2FA(w, r, p, s.setupData(p, sec, p.T("ui.2fa.bad_code")), http.StatusBadRequest)
		return
	}
	plain, hashes, err := s.recoverySet(ctx)
	if err == nil {
		err = s.Store.EnableTOTP(ctx, p.User.ID, step, hashes)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "user.2fa_on", p.User.Name, s.clientIP(r))
	s.render2FA(w, r, p, twoFAData{Codes: plain, On: true, Purpose: "codes"}, http.StatusOK)
}

// checkSecondFactor accepts a current TOTP code or an unused recovery code for a person with 2FA on, spending what it
// accepts. how is "totp" or "recovery".
func (s *Server) checkSecondFactor(ctx context.Context, userID int64, input string) (how string, ok bool) {
	tf, err := s.Store.TwoFactor(ctx, userID)
	if err != nil || tf.On == "" || s.Mirror == nil || s.Mirror.Key == nil {
		return "", false
	}
	if sec, err := s.Mirror.Key.Open(tf.Sealed); err == nil {
		if step, good := auth.VerifyTOTP(sec, input, time.Now(), int(s.settingInt(ctx, "auth.totp_skew_steps", 1)), tf.LastStep); good {
			if s.Store.UseTOTPStep(ctx, userID, step) == nil {
				return "totp", true
			}
			return "", false
		}
	}
	if n := auth.NormalizeRecovery(input); len(n) == 10 {
		if s.Store.UseRecoveryCode(ctx, userID, store.RecoveryHash(n)) == nil {
			return "recovery", true
		}
	}
	return "", false
}

// ui2FADisable switches 2FA off; it asks for the password and a code, so a borrowed open session cannot do it alone.
func (s *Server) ui2FADisable(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	hash, err := s.Store.PasswordHash(ctx, p.User.ID)
	if err != nil || auth.Verify(r.PostFormValue("password"), hash) != nil {
		s.setFlash(w, "ralat", p.T("ui.2fa.need_both"))
		http.Redirect(w, r, "/akaun#dualangkah", http.StatusSeeOther)
		return
	}
	if _, good := s.checkSecondFactor(ctx, p.User.ID, r.PostFormValue("code")); !good {
		s.setFlash(w, "ralat", p.T("ui.2fa.need_both"))
		http.Redirect(w, r, "/akaun#dualangkah", http.StatusSeeOther)
		return
	}
	if err := s.Store.DisableTOTP(ctx, p.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "user.2fa_off", p.User.Name, s.clientIP(r))
	s.setFlash(w, "berjaya", p.T("ui.2fa.off_done"))
	http.Redirect(w, r, "/akaun#dualangkah", http.StatusSeeOther)
}

// ui2FACodes issues a new set of recovery codes (a current code is required).
func (s *Server) ui2FACodes(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if _, good := s.checkSecondFactor(ctx, p.User.ID, r.PostFormValue("code")); !good {
		s.setFlash(w, "ralat", p.T("ui.2fa.bad_code"))
		http.Redirect(w, r, "/akaun#dualangkah", http.StatusSeeOther)
		return
	}
	plain, hashes, err := s.recoverySet(ctx)
	if err == nil {
		err = s.Store.ReplaceRecoveryCodes(ctx, p.User.ID, hashes)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(ctx, p.User.ID, "user.2fa_codes", p.User.Name, "")
	s.render2FA(w, r, p, twoFAData{Codes: plain, On: true, Purpose: "codes"}, http.StatusOK)
}

// startChallenge is called by the password step when the person has 2FA on.
func (s *Server) startChallenge(w http.ResponseWriter, r *http.Request, u store.User, next, ip string) error {
	ttl := time.Duration(s.settingInt(r.Context(), "auth.challenge_minutes", 5)) * time.Minute
	token, err := s.Store.CreateChallenge(r.Context(), u.ID, next, ip, ttl)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: cookieChallenge, Value: token, Path: "/login", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: int(ttl / time.Second)})
	http.Redirect(w, r, "/login/kod", http.StatusSeeOther)
	return nil
}

func (s *Server) uiLoginCodeForm(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	c, err := r.Cookie(cookieChallenge)
	if err != nil || !s.Store.ChallengeValid(r.Context(), c.Value, int(s.settingInt(r.Context(), "auth.challenge_attempts", 5))) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	p.Title = p.T("ui.2fa.login_title") + " · " + p.SiteName
	s.render(w, r, http.StatusOK, "login_code.html", p.with(loginData{}))
}

func (s *Server) uiLoginCode(w http.ResponseWriter, r *http.Request) {
	p := s.newPage(w, r, "")
	p.Title = p.T("ui.2fa.login_title") + " · " + p.SiteName
	ctx, ip := r.Context(), s.clientIP(r)
	if !s.checkCSRF(r) {
		s.render(w, r, http.StatusBadRequest, "login_code.html", p.with(loginData{Error: p.T("ui.login.csrf")}))
		return
	}
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		s.render(w, r, http.StatusTooManyRequests, "login_code.html", p.with(loginData{Error: p.T("http.too_many")}))
		return
	}
	ck, err := r.Cookie(cookieChallenge)
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	maxA := int(s.settingInt(ctx, "auth.challenge_attempts", 5))
	ch, err := s.Store.TakeChallengeAttempt(ctx, ck.Value, maxA)
	if err != nil { // expired, unknown or out of attempts: start again from the password
		http.SetCookie(w, &http.Cookie{Name: cookieChallenge, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true, Secure: s.UI.SecureCookies})
		s.setFlash(w, "ralat", p.T("ui.2fa.challenge_over"))
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	u, err := s.Store.UserByID(ctx, ch.UserID)
	how, good := "", false
	if err == nil && !u.Disabled {
		how, good = s.checkSecondFactor(ctx, u.ID, r.PostFormValue("code"))
	}
	if !good {
		if s.Limiter != nil {
			s.Limiter.Fail(ip)
		}
		s.Log.Warn("login code failed", "user", u.Name, "ip", ip)
		left := maxA - ch.Attempts
		s.render(w, r, http.StatusUnauthorized, "login_code.html", p.with(loginData{Error: p.N("ui.2fa.wrong_left", left)}))
		return
	}
	s.Store.DeleteChallenge(ctx, ck.Value)
	http.SetCookie(w, &http.Cookie{Name: cookieChallenge, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true, Secure: s.UI.SecureCookies})
	token, _, err := s.Store.CreateSession(ctx, u.ID, s.UI.SessionTTL, r.UserAgent(), ip)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieSession, Value: token, Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: int(s.UI.SessionTTL / time.Second)})
	_ = s.Store.Audit(ctx, u.ID, "user.login", u.Name, ip+" "+how)
	if how == "recovery" {
		s.setFlash(w, "ralat", p.T("ui.2fa.used_recovery"))
	}
	http.Redirect(w, r, safeNext(ch.Next), http.StatusSeeOther)
}

// uiSessionEnd signs out one session, or every other one ("lain").
func (s *Server) uiSessionEnd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	cur := ""
	if c, err := r.Cookie(cookieSession); err == nil {
		cur = c.Value
	}
	if strings.HasSuffix(r.URL.Path, "/lain") {
		n, err := s.Store.EndOtherSessions(r.Context(), p.User.ID, cur)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		_ = s.Store.Audit(r.Context(), p.User.ID, "user.sessions_end", p.User.Name, strconv.FormatInt(n, 10))
		s.setFlash(w, "berjaya", p.N("ui.sessions.ended_n", int(n)))
	} else {
		if err := s.Store.EndSession(r.Context(), p.User.ID, r.PostFormValue("id")); err != nil {
			s.errPage(w, r, http.StatusNotFound)
			return
		}
		_ = s.Store.Audit(r.Context(), p.User.ID, "user.session_end", p.User.Name, "")
		s.setFlash(w, "berjaya", p.T("ui.sessions.ended"))
	}
	http.Redirect(w, r, "/akaun#sesi", http.StatusSeeOther)
}
