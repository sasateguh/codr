package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

// fakeSMTP is just enough of an SMTP server over implicit TLS to receive one message per connection.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	msgs []string
	auth []string
	pool *x509.CertPool
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	f := &fakeSMTP{pool: x509.NewCertPool()}
	f.pool.AddCert(cert)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = append(f.auth, strings.TrimSpace(line[len("AUTH PLAIN"):]))
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.msgs...)
}

func TestMailSendDue(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "codr.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	keyHex, _ := secret.Generate()
	key, _ := secret.Parse(keyHex)
	owner, _ := st.CreateUser(ctx, "alice", "alice@contoh.invalid", "x", false, "ms-MY")
	bob, _ := st.CreateUser(ctx, "bob", "bob@contoh.invalid", "x", false, "en-US")
	repo, _ := st.CreateRepo(ctx, owner.ID, "awam", "", false, "main")
	srv := newFakeSMTP(t)
	_, port, _ := net.SplitHostPort(srv.ln.Addr().String())
	p, _ := strconv.Atoi(port)
	sealed, _ := key.Seal("rahsia-smtp")
	m := &Mailer{HTTPHost: "https://git.contoh.invalid", TLS: &tls.Config{RootCAs: srv.pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}}
	row := store.NotifyRow{UserID: bob.ID, RepoID: repo.ID, Number: 7, ActorID: owner.ID, Thread: "pull", Reason: "mention", Kind: "issue_comment",
		Action: "commented", Title: "Betulkan\r\nBcc: mangsa@contoh.invalid", Excerpt: "Sila semak baris 3 @bob."}

	// mail off: due rows are cleared, nothing is sent, nothing piles up for later
	_ = st.UpsertNotification(ctx, row)
	if err := m.SendDue(ctx, st, key); err != nil {
		t.Fatal(err)
	}
	if due, _ := st.MailDue(ctx, 10); len(due) != 0 || len(srv.got()) != 0 {
		t.Fatalf("mail off kept a backlog: %d", len(due))
	}

	_ = st.SaveMailConfig(ctx, store.MailConfig{Enabled: true, Host: "127.0.0.1", Port: p, Security: "tls", Username: "codr@contoh.invalid",
		PasswordSealed: sealed, From: "Codr <codr@contoh.invalid>"})
	_ = st.UpsertNotification(ctx, row)
	if err := m.SendDue(ctx, st, key); err != nil {
		t.Fatal(err)
	}
	got := srv.got()
	if len(got) != 1 {
		t.Fatalf("sent %d", len(got))
	}
	msg := got[0]
	head, body, _ := strings.Cut(msg, "\r\n\r\n")
	for _, want := range []string{"To: <bob@contoh.invalid>", "From: \"Codr\" <codr@contoh.invalid>", "In-Reply-To: <alice.awam.pull.7@contoh.invalid>",
		"List-ID: <awam.alice.contoh.invalid>", "List-Unsubscribe: <https://git.contoh.invalid/akaun>", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q missing:\n%s", want, head)
		}
	}
	for _, l := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(l), "bcc:") {
			t.Fatalf("header injection through the title: %q", l)
		}
	}
	plain := strings.ReplaceAll(body, "=\r\n", "")
	for _, want := range []string{"alice commented on pull request #7 in alice/awam.", "Sila semak baris 3 @bob.",
		"https://git.contoh.invalid/alice/awam/pr/7", "because you were mentioned"} {
		if !strings.Contains(plain, want) {
			t.Errorf("body %q missing:\n%s", want, plain)
		}
	}
	if len(srv.auth) != 1 || srv.auth[0] == "" {
		t.Fatal("no AUTH")
	}
	if cfg, _ := st.MailConfig(ctx); cfg.LastOKAt == "" || cfg.LastError != "" {
		t.Fatalf("result not recorded: %+v", cfg)
	}

	// the same thread moves again inside the cooldown: no second mail yet, it stays due
	_ = st.UpsertNotification(ctx, row)
	_ = m.SendDue(ctx, st, key)
	if len(srv.got()) != 1 {
		t.Fatal("cooldown ignored")
	}
	if due, _ := st.MailDue(ctx, 10); len(due) != 1 {
		t.Fatal("cooled-down mail dropped instead of waiting")
	}
	m.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	_ = m.SendDue(ctx, st, key)
	if len(srv.got()) != 2 {
		t.Fatal("not sent after the cooldown")
	}

	// reading the notification cancels its email; a person who turned email off gets none
	_ = st.UpsertNotification(ctx, row)
	_ = st.MarkRead(ctx, bob.ID, 0)
	m.now = func() time.Time { return time.Now().Add(time.Hour) }
	_ = m.SendDue(ctx, st, key)
	if len(srv.got()) != 2 {
		t.Fatal("mail for a read notification")
	}
	_ = st.SetEmailSettings(ctx, bob.ID, "bob@contoh.invalid", false)
	_ = st.UpsertNotification(ctx, row)
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	_ = m.SendDue(ctx, st, key)
	if len(srv.got()) != 2 {
		t.Fatal("mail despite email off")
	}

	// a server that refuses records the error and keeps the mail due
	_ = st.SetEmailSettings(ctx, bob.ID, "bob@contoh.invalid", true)
	_ = st.SaveMailConfig(ctx, store.MailConfig{Enabled: true, Host: "127.0.0.1", Port: 1, Security: "tls", From: "codr@contoh.invalid"})
	_ = st.UpsertNotification(ctx, row)
	m.now = func() time.Time { return time.Now().Add(3 * time.Hour) }
	_ = m.SendDue(ctx, st, key)
	if cfg, _ := st.MailConfig(ctx); cfg.LastError == "" {
		t.Fatal("error not recorded")
	}
	if due, _ := st.MailDue(ctx, 10); len(due) != 1 {
		t.Fatal("failed mail dropped")
	}
}

// TestReasonsMatchRank: the exported list of reasons is exactly the ranked set the notifier uses.
func TestReasonsMatchRank(t *testing.T) {
	if len(Reasons) != len(rank) {
		t.Fatalf("Reasons %v vs rank %v", Reasons, rank)
	}
	for _, r := range Reasons {
		if _, ok := rank[r]; !ok {
			t.Fatalf("reason %q has no rank", r)
		}
	}
}
