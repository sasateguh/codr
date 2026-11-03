package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// Mailer sends notification email through the server in mail_config (D79). Plain text only: no tracking, no images,
// no HTML from other people's comments in someone's inbox.
type Mailer struct {
	HTTPHost string // forge base URL, for links
	// TLS overrides the TLS settings (tests with a self-signed server); nil = the system roots and the host name.
	TLS *tls.Config
	// now is replaceable in tests
	now func() time.Time
}

func (m *Mailer) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Message is one email ready to send.
type Message struct {
	To, Subject, Body string
	ThreadID          string // stable per issue/PR ("owner/repo/issue/3"), so mail clients group the thread
	Owner, Repo       string // for List-ID, so people can filter one repository
}

// SendDue sends every notification email that is due, respecting the per-thread cooldown.
func (m *Mailer) SendDue(ctx context.Context, st *store.Store, key secret.Key) error {
	cfg, err := st.MailConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.Enabled || cfg.Host == "" || cfg.From == "" {
		return st.ClearMailDue(ctx) // switching mail on later must not send a backlog
	}
	limit, _ := st.SettingInt(ctx, "mail.per_tick")
	cool, _ := st.SettingInt(ctx, "mail.thread_cooldown_minutes")
	due, err := st.MailDue(ctx, int(max(limit, 1)))
	if err != nil {
		return err
	}
	for _, it := range due {
		if it.Email == "" {
			_ = st.MailDone(ctx, it.ID, false)
			continue
		}
		if on, err := st.EmailNotify(ctx, it.UserID); err != nil || !on {
			_ = st.MailDone(ctx, it.ID, false)
			continue
		}
		if it.EmailedAt != "" {
			if t, err := time.Parse("2006-01-02T15:04:05.000Z", it.EmailedAt); err == nil && m.clock().Sub(t) < time.Duration(cool)*time.Minute {
				continue // stays due: one mail carries the latest state when the cooldown ends
			}
		}
		msg := m.Compose(it)
		if err := m.Send(ctx, st, cfg, key, msg); err != nil {
			_ = st.MailResult(ctx, err.Error())
			return nil // the server is the problem, not this item: try the queue again next tick
		}
		_ = st.MailResult(ctx, "")
		if err := st.MailDone(ctx, it.ID, true); err != nil {
			return err
		}
	}
	return nil
}

// Compose writes the email for one notification, in the person's language.
func (m *Mailer) Compose(it store.MailItem) Message {
	lang := it.Lang
	full := it.Owner + "/" + it.Repo
	path := "/isu/"
	if it.Thread == "pull" {
		path = "/pr/"
	}
	url := m.HTTPHost + "/" + full + path + strconv.FormatInt(it.Number, 10)
	actor := it.Actor
	if actor == "" {
		actor = i18n.T(lang, "mail.someone")
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "mail.action."+it.LastAction+"."+it.Thread, actor, it.Number, full))
	b.WriteString("\n\n" + it.Title + "\n")
	if it.Excerpt != "" {
		b.WriteString("\n" + it.Excerpt + "\n")
	}
	b.WriteString("\n" + url + "\n\n")
	b.WriteString(i18n.T(lang, "mail.reason."+it.Reason) + "\n")
	b.WriteString(i18n.T(lang, "mail.manage", m.HTTPHost+"/akaun") + "\n")
	return Message{To: it.Email, Subject: "[" + full + "] " + it.Title + " (#" + strconv.FormatInt(it.Number, 10) + ")", Body: b.String(),
		ThreadID: full + "/" + it.Thread + "/" + strconv.FormatInt(it.Number, 10), Owner: it.Owner, Repo: it.Repo}
}

// clean removes what could start a new header line from a value placed in a header.
func clean(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func msgID(host string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "<" + hex.EncodeToString(b) + "@" + host + ">"
}

// Build renders the RFC 5322 message.
func (m *Mailer) Build(from string, msg Message) ([]byte, error) {
	fa, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("mail: from address: %w", err)
	}
	ta, err := mail.ParseAddress(msg.To)
	if err != nil {
		return nil, fmt.Errorf("mail: to address: %w", err)
	}
	host := fa.Address[strings.LastIndex(fa.Address, "@")+1:]
	var buf bytes.Buffer
	h := func(k, v string) { buf.WriteString(k + ": " + clean(v) + "\r\n") }
	h("From", fa.String())
	h("To", ta.String())
	h("Subject", mime.QEncoding.Encode("utf-8", clean(msg.Subject)))
	h("Date", m.clock().UTC().Format(time.RFC1123Z))
	h("Message-ID", msgID(host))
	if msg.ThreadID != "" {
		ref := "<" + strings.ReplaceAll(msg.ThreadID, "/", ".") + "@" + host + ">"
		h("In-Reply-To", ref)
		h("References", ref)
	}
	if msg.Repo != "" {
		h("List-ID", "<"+msg.Repo+"."+msg.Owner+"."+host+">")
	}
	h("List-Unsubscribe", "<"+m.HTTPHost+"/akaun>")
	h("Auto-Submitted", "auto-generated")
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "quoted-printable")
	buf.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&buf)
	if _, err := qp.Write([]byte(strings.ReplaceAll(msg.Body, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrNotConfigured: the mail server is not filled in.
var ErrNotConfigured = errors.New("mail: server not configured")

// Send delivers one message through the configured server.
func (m *Mailer) Send(ctx context.Context, st *store.Store, cfg store.MailConfig, key secret.Key, msg Message) error {
	if cfg.Host == "" || cfg.From == "" {
		return ErrNotConfigured
	}
	pass := ""
	if cfg.PasswordSealed != "" {
		var err error
		if pass, err = key.Open(cfg.PasswordSealed); err != nil {
			return errors.New("mail: password cannot be opened (auth.secret_key changed?)")
		}
	}
	raw, err := m.Build(cfg.From, msg)
	if err != nil {
		return err
	}
	timeout := 20 * time.Second
	if v, err := st.SettingInt(ctx, "mail.timeout_seconds"); err == nil && v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	tc := m.TLS
	if tc == nil {
		tc = &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	}
	d := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	if cfg.Security == "tls" {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tc)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if cfg.Security == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mail: server does not offer STARTTLS")
		}
		if err := c.StartTLS(tc); err != nil {
			return err
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, pass, cfg.Host)); err != nil {
			return err
		}
	}
	fa, _ := mail.ParseAddress(cfg.From)
	ta, _ := mail.ParseAddress(msg.To)
	if err := c.Mail(fa.Address); err != nil {
		return err
	}
	if err := c.Rcpt(ta.Address); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
