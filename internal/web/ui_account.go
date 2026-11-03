package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// maxKeyBytes bounds a pasted public key; a real one is well under 1 KiB.
const maxKeyBytes = 16 * 1024

// accountData is what account.html renders: the viewer's keys and tokens, the forms'
// previous input on error, and a freshly created token shown exactly once (never put in
// a cookie or the DB in clear).
type accountData struct {
	Keys           []store.SSHKey
	Tokens         []store.AccessToken
	NewToken       string
	SSHHost        string
	HTTPHost       string
	SSHHint        string
	Error          string
	KeyTitle       string
	KeyPub         string
	TokenName      string
	DefaultDays    int64
	MaxDays        int64
	EmailNotify    bool // D79
	TwoFA          store.TwoFactor
	Sessions       []store.SessionRow
	Scopes         []scopeOpt
	PasswordMinLen int
	OIDCLinks      []store.OIDCLink // D91
}

// scopeOpt is one checkbox: the scope value and its locale key.
type scopeOpt struct{ Value, Key string }

// requireUser is requireAdmin without the admin check: any signed-in user.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (page, bool) {
	p := s.newPage(w, r, "")
	if p.User == nil {
		http.Redirect(w, r, "/login?next="+r.URL.Path, http.StatusSeeOther)
		return p, false
	}
	return p, true
}

func (s *Server) renderAccount(w http.ResponseWriter, r *http.Request, p page, d accountData, code int) {
	var err error
	d.EmailNotify, _ = s.Store.EmailNotify(r.Context(), p.User.ID)
	d.TwoFA, _ = s.Store.TwoFactor(r.Context(), p.User.ID)
	cur := ""
	if c, err := r.Cookie(cookieSession); err == nil {
		cur = c.Value
	}
	d.Sessions, _ = s.Store.Sessions(r.Context(), p.User.ID, cur)
	if d.Keys, err = s.Store.SSHKeys(r.Context(), p.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Tokens, err = s.Store.Tokens(r.Context(), p.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.DefaultDays, _ = s.Store.SettingInt(r.Context(), "token.default_days")
	d.MaxDays, _ = s.Store.SettingInt(r.Context(), "token.max_days")
	d.SSHHint, _ = s.Store.Setting(r.Context(), "ui.ssh_hint")
	d.SSHHost, d.HTTPHost = s.UI.SSHHost, s.UI.HTTPHost
	d.PasswordMinLen = s.UI.PasswordMinLen
	d.OIDCLinks, _ = s.Store.OIDCLinks(r.Context(), p.User.ID)
	for _, sc := range []string{store.ScopeRead, store.ScopeWrite, store.ScopeAdmin} {
		if sc == store.ScopeAdmin && !p.User.IsAdmin {
			continue
		}
		d.Scopes = append(d.Scopes, scopeOpt{Value: sc, Key: "ui.account.scope." + sc})
	}
	p.Title = p.T("ui.account.title") + " · " + p.SiteName
	s.render(w, r, code, "account.html", p.with(d))
}

func (s *Server) uiAccount(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	s.takeFlash(w, r, &p)
	s.renderAccount(w, r, p, accountData{}, http.StatusOK)
}

func (s *Server) uiAccountKeyAdd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	raw := strings.TrimSpace(r.PostFormValue("key"))
	if len(raw) > maxKeyBytes {
		raw = ""
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		s.renderAccount(w, r, p, accountData{Error: p.T("ui.account.key_invalid"), KeyTitle: title, KeyPub: raw}, http.StatusBadRequest)
		return
	}
	if title == "" {
		title = comment
	}
	if title == "" {
		title = pub.Type()
	}
	fp := ssh.FingerprintSHA256(pub)
	k, err := s.Store.AddSSHKey(r.Context(), p.User.ID, title, fp, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.renderAccount(w, r, p, accountData{Error: p.T("ui.account.key_duplicate"), KeyTitle: title, KeyPub: raw}, http.StatusBadRequest)
			return
		}
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "key.add", p.User.Name, fp)
	s.setFlash(w, "berjaya", p.T("ui.account.key_added", k.Fingerprint))
	http.Redirect(w, r, "/akaun", http.StatusSeeOther)
}

func (s *Server) uiAccountKeyRemove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	err := s.Store.DeleteSSHKey(r.Context(), p.User.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound) || id <= 0:
		s.setFlash(w, "ralat", p.T("ui.account.not_found"))
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		_ = s.Store.Audit(r.Context(), p.User.ID, "key.remove", p.User.Name, strconv.FormatInt(id, 10))
		s.setFlash(w, "berjaya", p.T("ui.account.key_removed", id))
	}
	http.Redirect(w, r, "/akaun", http.StatusSeeOther)
}

func (s *Server) uiAccountTokenAdd(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		name = "web " + store.Now()[:10]
	}
	bad := func(msg string) {
		s.renderAccount(w, r, p, accountData{Error: msg, TokenName: name}, http.StatusBadRequest)
	}
	// scopes come as repeated checkbox values; admin is only offered to admins and only honoured for them
	var picked []string
	for _, sc := range r.PostForm["scope"] {
		if sc == store.ScopeAdmin && !p.User.IsAdmin {
			continue
		}
		picked = append(picked, sc)
	}
	scopes, ok := store.ParseScopes(strings.Join(picked, ","))
	if !ok {
		bad(p.T("ui.account.bad_scopes"))
		return
	}
	maxDays, err := s.Store.SettingInt(r.Context(), "token.max_days")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	days, _ := strconv.ParseInt(strings.TrimSpace(r.PostFormValue("days")), 10, 64)
	if days == 0 {
		if days, err = s.Store.SettingInt(r.Context(), "token.default_days"); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if days < 1 || days > maxDays {
		bad(p.T("ui.account.bad_days", maxDays))
		return
	}
	raw, t, err := s.Store.CreateToken(r.Context(), p.User.ID, name, scopes, time.Duration(days)*24*time.Hour)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "token.create", p.User.Name, t.Name+" "+strings.Join(scopes, ","))
	s.renderAccount(w, r, p, accountData{NewToken: raw}, http.StatusOK)
}

func (s *Server) uiAccountTokenRemove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	id, _ := strconv.ParseInt(r.PostFormValue("id"), 10, 64)
	err := s.Store.DeleteToken(r.Context(), p.User.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound) || id <= 0:
		s.setFlash(w, "ralat", p.T("ui.account.not_found"))
	case err != nil:
		s.fail(w, r, err)
		return
	default:
		_ = s.Store.Audit(r.Context(), p.User.ID, "token.revoke", p.User.Name, strconv.FormatInt(id, 10))
		s.setFlash(w, "berjaya", p.T("ui.account.token_removed", id))
	}
	http.Redirect(w, r, "/akaun", http.StatusSeeOther)
}

// uiAccountPassword changes the viewer's own password (FR-01): the current password must
// verify first, the new one must meet auth.password_min_len and be typed twice. Git over
// HTTPS is untouched (tokens only, D32); SSH keys are untouched.
func (s *Server) uiAccountPassword(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	bad := func(msg string) {
		s.renderAccount(w, r, p, accountData{Error: msg}, http.StatusBadRequest)
	}
	current, next, again := r.PostFormValue("current"), r.PostFormValue("password"), r.PostFormValue("again")
	hash, err := s.Store.PasswordHash(r.Context(), p.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch {
	case auth.Verify(current, hash) != nil:
		bad(p.T("ui.account.pw_wrong"))
		return
	case len(next) < s.UI.PasswordMinLen:
		bad(p.T("ui.account.pw_short", s.UI.PasswordMinLen))
		return
	case next != again:
		bad(p.T("ui.account.pw_mismatch"))
		return
	}
	h, err := auth.Hash(next, s.UI.Argon)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.Store.SetPasswordHash(r.Context(), p.User.ID, h); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "user.password", p.User.Name, "web")
	s.setFlash(w, "berjaya", p.T("ui.account.pw_changed"))
	http.Redirect(w, r, "/akaun#katalaluan", http.StatusSeeOther)
}
