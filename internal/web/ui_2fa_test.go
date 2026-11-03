package web

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/auth"
)

var (
	secretTextRE = regexp.MustCompile(`class="rahsia-totp mono" tabindex="0">([A-Z2-7 ]+)<`)
	recoveryRE   = regexp.MustCompile(`<li>([a-z2-7]{5}-[a-z2-7]{5})</li>`)
)

// signedIn reports whether the browser has a session (the account page answers instead of sending to /login).
func signedIn(b *browser) bool {
	code, _ := b.get("/akaun", "en")
	return code == 200
}

func TestTwoFactor(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	h.s.Limiter = &Limiter{Max: 100, Window: time.Minute} // the per-challenge limit is tested first; the IP limit at the end
	a1 := h.login("alice", "kata laluan alice")

	// setup: a QR code and the key; reloading keeps the same key until it is confirmed
	_, body := a1.get("/akaun/2fa", "en")
	m := secretTextRE.FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, `<svg class="qr"`) {
		t.Fatalf("setup page:\n%s", body)
	}
	sec := strings.ReplaceAll(m[1], " ", "")
	if _, again := a1.get("/akaun/2fa", "en"); !strings.Contains(again, m[1]) {
		t.Fatal("the key changed on reload")
	}
	if code, _, _ := a1.post("/akaun/2fa", url.Values{"code": {"000000"}}); code != 400 {
		t.Fatal("wrong confirmation code accepted")
	}
	now := auth.TOTPStep(time.Now())
	c0, _ := auth.TOTPCode(sec, now)
	code, body, _ := a1.post("/akaun/2fa", url.Values{"code": {c0}})
	codes := recoveryRE.FindAllStringSubmatch(body, -1)
	if code != 200 || len(codes) != 10 {
		t.Fatalf("confirm: %d, %d codes", code, len(codes))
	}
	if _, acc := a1.get("/akaun", "en"); !strings.Contains(acc, "10 recovery codes left") || recoveryRE.MatchString(acc) {
		t.Fatal("account page: state, or codes shown again")
	}

	// the password alone signs nothing in
	login := func(b *browser) {
		b.get("/login", "en")
		if code, _, loc := b.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 || loc != "/login/kod" {
			t.Fatalf("password step: %d %s", code, loc)
		}
	}
	a2 := h.browser()
	login(a2)
	if signedIn(a2) {
		t.Fatal("signed in after the password alone")
	}
	if code, _ := a2.get("/login/kod", "en"); code != 200 {
		t.Fatal("code page")
	}
	// the code that confirmed setup cannot be replayed; the next step's code works
	if code, body, _ := a2.post("/login/kod", url.Values{"code": {c0}}); code != 401 || !strings.Contains(body, "4 tries left") {
		t.Fatalf("replay: %d", code)
	}
	c1, _ := auth.TOTPCode(sec, now+1)
	if code, _, loc := a2.post("/login/kod", url.Values{"code": {c1}}); code != 303 || loc != "/" || !signedIn(a2) {
		t.Fatalf("code step: %d %s", code, loc)
	}
	// and that one cannot be used again for another sign-in
	a3 := h.browser()
	login(a3)
	if code, _, _ := a3.post("/login/kod", url.Values{"code": {c1}}); code != 401 || signedIn(a3) {
		t.Fatal("a used code signed in again")
	}
	// a recovery code works once, in any case and with or without its dash
	rc := codes[0][1]
	if code, _, _ := a3.post("/login/kod", url.Values{"code": {strings.ToUpper(strings.ReplaceAll(rc, "-", ""))}}); code != 303 || !signedIn(a3) {
		t.Fatal("recovery code")
	}
	a4 := h.browser()
	login(a4)
	if code, _, _ := a4.post("/login/kod", url.Values{"code": {rc}}); code != 401 {
		t.Fatal("recovery code used twice")
	}
	// out of attempts: back to the password, the challenge is gone
	for i := 0; i < 4; i++ { // attempts 2..5 of auth.challenge_attempts = 5: still answered
		if code, _, _ := a4.post("/login/kod", url.Values{"code": {"111111"}}); code != 401 {
			t.Fatalf("attempt %d: %d", i+2, code)
		}
	}
	if code, _, loc := a4.post("/login/kod", url.Values{"code": {"111111"}}); code != 303 || loc != "/login" {
		t.Fatalf("after five wrong codes: %d %s", code, loc)
	}
	if code, _, loc := a4.post("/login/kod", url.Values{"code": {"222222"}}); code != 303 || loc != "/login" || signedIn(a4) {
		t.Fatalf("a used-up challenge answered again: %d %s", code, loc)
	}

	// git over HTTPS with a token is not affected
	w := h.work()
	h.commit(w, "dua.txt", "x\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "HEAD:refs/heads/dua"); err != nil {
		t.Fatalf("token push with 2FA on: %s", out)
	}

	// sessions: a1, a2 and a3 are signed in; a2 signs out everywhere else
	_, acc := a2.get("/akaun", "en")
	if strings.Count(acc, `action="/akaun/sesi/satu"`) != 2 || !strings.Contains(acc, "This browser") {
		t.Fatal("session list")
	}
	a2.post("/akaun/sesi/lain", url.Values{})
	if signedIn(a1) || signedIn(a3) || !signedIn(a2) {
		t.Fatal("sign out everywhere else")
	}
	// bob cannot end alice's session by its id
	bob := h.login("bob", "kata laluan bob panjang")
	sess, _ := h.st.Sessions(ctx, h.users["alice"].ID, "")
	if code, _, _ := bob.post("/akaun/sesi/satu", url.Values{"id": {sess[0].ID}}); code != 404 || !signedIn(a2) {
		t.Fatal("ended another person's session")
	}

	// wrong codes count against the sign-in limit of the address, like wrong passwords: after it, even a right code waits
	h.s.Limiter = &Limiter{Max: 2, Window: time.Minute}
	a6 := h.browser()
	login(a6)
	a6.post("/login/kod", url.Values{"code": {"111111"}})
	a6.post("/login/kod", url.Values{"code": {"111111"}})
	c2, _ := auth.TOTPCode(sec, now+2)
	if code, _, _ := a6.post("/login/kod", url.Values{"code": {c2}}); code != 429 || signedIn(a6) {
		t.Fatalf("code limit by address: %d", code)
	}
	h.s.Limiter = &Limiter{Max: 100, Window: time.Minute}
	// turning it off needs the password and a code
	cOff, _ := auth.TOTPCode(sec, now-1) // older than the last used step: refused
	if _, _, _ = a2.post("/akaun/2fa/matikan", url.Values{"password": {"kata laluan alice"}, "code": {cOff}}); true {
		if tf, _ := h.st.TwoFactor(ctx, h.users["alice"].ID); tf.On == "" {
			t.Fatal("switched off with an old code")
		}
	}
	a2.post("/akaun/2fa/matikan", url.Values{"password": {"salah"}, "code": {codes[1][1]}})
	if tf, _ := h.st.TwoFactor(ctx, h.users["alice"].ID); tf.On == "" {
		t.Fatal("switched off with a wrong password")
	}
	a2.post("/akaun/2fa/matikan", url.Values{"password": {"kata laluan alice"}, "code": {codes[1][1]}})
	if tf, _ := h.st.TwoFactor(ctx, h.users["alice"].ID); tf.On != "" || tf.Unused != 0 {
		t.Fatalf("not switched off: %+v", tf)
	}
	a5 := h.browser()
	a5.get("/login", "en")
	if code, _, loc := a5.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 || loc == "/login/kod" || !signedIn(a5) {
		t.Fatalf("password sign-in after switching off: %s", loc)
	}
}
