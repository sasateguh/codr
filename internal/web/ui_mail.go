package web

import (
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"codr.my/codr/internal/notify"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// /admin/emel: the outgoing mail server for notification email (D79). The password is sealed under auth.secret_key and
// never shown again; leaving the field empty keeps it. "Send test" mails the admin's own address and shows the
// server's answer, so a wrong port or password is found here and not by a missing notification.

type mailData struct {
	Config   store.MailConfig
	HasPass  bool
	NoKey    bool
	MyEmail  string
	Error    string
	Security []string
}

func (s *Server) mailer() *notify.Mailer {
	if s.Notifier != nil && s.Notifier.Mailer != nil {
		return s.Notifier.Mailer
	}
	return &notify.Mailer{HTTPHost: s.UI.HTTPHost}
}

func (s *Server) renderMail(w http.ResponseWriter, r *http.Request, p page, d mailData, code int) {
	cfg, err := s.Store.MailConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Config.Host == "" && d.Error == "" {
		d.Config = cfg
	}
	d.HasPass = cfg.PasswordSealed != ""
	d.NoKey = s.Mirror == nil || s.Mirror.Key == nil
	d.MyEmail = p.User.Email
	d.Security = []string{"tls", "starttls"}
	p.Title = p.T("ui.mail.title") + " · " + p.SiteName
	s.render(w, r, code, "mail.html", p.with(d))
}

func (s *Server) uiMailAdmin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	s.takeFlash(w, r, &p)
	s.renderMail(w, r, p, mailData{}, http.StatusOK)
}

func (s *Server) uiMailAdminSave(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	cur, err := s.Store.MailConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	port, _ := strconv.Atoi(r.PostFormValue("port"))
	c := store.MailConfig{Enabled: r.PostFormValue("enabled") == "1", Host: strings.TrimSpace(r.PostFormValue("host")), Port: port,
		Security: r.PostFormValue("security"), Username: strings.TrimSpace(r.PostFormValue("username")), From: strings.TrimSpace(r.PostFormValue("from")),
		PasswordSealed: cur.PasswordSealed}
	bad := func(key string) { s.renderMail(w, r, p, mailData{Config: c, Error: p.T(key)}, http.StatusBadRequest) }
	if c.Security != "tls" && c.Security != "starttls" {
		c.Security = "tls"
	}
	if port < 1 || port > 65535 || strings.ContainsAny(c.Host, " /\r\n") {
		bad("ui.mail.bad_server")
		return
	}
	if c.From != "" {
		if _, err := mail.ParseAddress(c.From); err != nil {
			bad("ui.mail.bad_from")
			return
		}
	}
	if c.Enabled && (c.Host == "" || c.From == "") {
		bad("ui.mail.need_server")
		return
	}
	if pw := r.PostFormValue("password"); pw != "" {
		if s.Mirror == nil || s.Mirror.Key == nil {
			bad("ui.hook.no_key")
			return
		}
		if c.PasswordSealed, err = s.Mirror.Key.Seal(pw); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if r.PostFormValue("clear_password") == "1" {
		c.PasswordSealed = ""
	}
	if err := s.Store.SaveMailConfig(r.Context(), c); err != nil {
		s.fail(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), p.User.ID, "mail.config", c.Host, strconv.FormatBool(c.Enabled))
	s.setFlash(w, "berjaya", p.T("ui.mail.saved"))
	http.Redirect(w, r, "/admin/emel", http.StatusSeeOther)
}

func (s *Server) uiMailAdminTest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	if !s.checkCSRF(r) {
		s.errPage(w, r, http.StatusForbidden)
		return
	}
	if p.User.Email == "" {
		s.renderMail(w, r, p, mailData{Error: p.T("ui.mail.need_my_email")}, http.StatusBadRequest)
		return
	}
	cfg, err := s.Store.MailConfig(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var key secret.Key
	if s.Mirror != nil {
		key = s.Mirror.Key
	}
	msg := notify.Message{To: p.User.Email, Subject: p.T("ui.mail.test_subject", p.SiteName), Body: p.T("ui.mail.test_body", s.UI.HTTPHost) + "\n"}
	if err := s.mailer().Send(r.Context(), s.Store, cfg, key, msg); err != nil {
		_ = s.Store.MailResult(r.Context(), err.Error())
		s.renderMail(w, r, p, mailData{Error: p.T("ui.mail.test_failed", err.Error())}, http.StatusBadGateway)
		return
	}
	_ = s.Store.MailResult(r.Context(), "")
	_ = s.Store.Audit(r.Context(), p.User.ID, "mail.test", p.User.Email, "")
	s.setFlash(w, "berjaya", p.T("ui.mail.test_sent", p.User.Email))
	http.Redirect(w, r, "/admin/emel", http.StatusSeeOther)
}
