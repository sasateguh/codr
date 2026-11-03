package web

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"codr.my/codr/internal/store"
)

// fakeIdP is an OpenID provider: discovery, JWKS, an authorize endpoint that signs in whoever the test says, and a
// token endpoint that checks the client secret, the redirect URI and the PKCE verifier before it signs an ID token.
type fakeIdP struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]map[string]string // code → challenge, nonce, redirect
	who   map[string]any               // the claims the next sign-in gets (sub, email, email_verified)
	aud   string                       // "" = the client id
	// tokenCalls counts the exchanges, to prove a refused flow never reached the provider
	tokenCalls int
}

func newFakeIdP(t *testing.T) *fakeIdP {
	f := &fakeIdP{codes: map[string]map[string]string{}}
	f.key, _ = rsa.GenerateKey(rand.Reader, 2048)
	b64 := base64.RawURLEncoding.EncodeToString
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks"})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "n": b64(f.key.N.Bytes()), "e": b64(big.NewInt(int64(f.key.E)).Bytes())}}})
	})
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "codr-test" || q.Get("response_type") != "code" {
			http.Error(w, "bad request", 400)
			return
		}
		code := "kod-" + q.Get("state")[:8]
		f.mu.Lock()
		f.codes[code] = map[string]string{"challenge": q.Get("code_challenge"), "nonce": q.Get("nonce"), "redirect": q.Get("redirect_uri")}
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.tokenCalls++
		id, sec, _ := r.BasicAuth()
		_ = r.ParseForm()
		c, ok := f.codes[r.PostFormValue("code")]
		delete(f.codes, r.PostFormValue("code"))
		h := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if id != "codr-test" || sec != "rahsia-klien" || !ok || b64(h[:]) != c["challenge"] || r.PostFormValue("redirect_uri") != c["redirect"] {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		aud := f.aud
		if aud == "" {
			aud = "codr-test"
		}
		claims := map[string]any{"iss": f.srv.URL, "aud": aud, "exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(), "nonce": c["nonce"]}
		for k, v := range f.who {
			claims[k] = v
		}
		hb, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
		cb, _ := json.Marshal(claims)
		in := b64(hb) + "." + b64(cb)
		d := sha256.Sum256([]byte(in))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, d[:])
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": in + "." + b64(sig), "token_type": "Bearer"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// oidcSignIn drives one sign-in: start → provider → callback. It returns the callback's status and Location.
func oidcSignIn(t *testing.T, b *browser, f *fakeIdP) (int, string, string) {
	t.Helper()
	resp, err := b.c.Get(b.h.srv.URL + "/login/oidc?next=/akaun")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.HasPrefix(resp.Header.Get("Location"), f.srv.URL+"/authorize?") {
		t.Fatalf("start: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, err = idpClient.Do(mustReq(resp.Header.Get("Location")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	code, body := b.get("/login/oidc/balik?"+back.RawQuery, "en")
	return code, body, back.RawQuery
}

func mustReq(u string) *http.Request { r, _ := http.NewRequest("GET", u, nil); return r }

// idpClient visits the provider like a browser would, but stops at its redirect back to Codr.
var idpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// TestOIDC (D91): off = no button and 404; on = the full code flow with PKCE against a provider that checks it; an
// account is found by VERIFIED email the first time and by the linked subject afterwards; nothing is created; a 2FA
// account still lands on the code page; a reused or foreign callback, a wrong audience, an unverified or unknown email
// and a disabled account are refused; unlinking works.
func TestOIDC(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	ctx := context.Background()
	h.s.OIDCInsecure = true
	h.s.Limiter = &Limiter{Max: 100, Window: time.Minute} // every refusal counts as a failed sign-in; this test makes many
	f := newFakeIdP(t)
	anon := h.browser()
	if _, body := anon.get("/login", "en"); strings.Contains(body, "/login/oidc") {
		t.Fatal("a provider button while it is off")
	}
	if code, _ := anon.get("/login/oidc", "en"); code != 404 {
		t.Fatalf("start while off: %d", code)
	}
	// configure through the admin page
	adminU := h.users["alice"]
	_, _ = h.st.DB().Exec(`UPDATE users SET is_admin = 1, email = 'alice@company.example' WHERE id = ?`, adminU.ID)
	alice := h.login("alice", "kata laluan alice")
	if code, body, _ := alice.post("/admin/oidc", url.Values{"name": {"Syarikat"}, "issuer": {f.srv.URL}, "client_id": {"codr-test"}, "enabled": {"1"}}); code != 400 || !strings.Contains(body, "Fill in the name") {
		t.Fatalf("on without a secret: %d", code)
	}
	if code, _, _ := alice.post("/admin/oidc", url.Values{"name": {"Syarikat"}, "issuer": {f.srv.URL}, "client_id": {"codr-test"}, "secret": {"rahsia-klien"}, "scopes": {"openid email"}, "enabled": {"1"}}); code != 303 {
		t.Fatalf("save: %d", code)
	}
	if c, _ := h.st.OIDCConfig(ctx); !c.Ready() || strings.Contains(c.SecretSealed, "rahsia-klien") {
		t.Fatalf("config: ready %v, secret sealed %v", c.Ready(), !strings.Contains(c.SecretSealed, "rahsia-klien"))
	}
	if _, _, loc := alice.post("/admin/oidc/uji", url.Values{}); loc != "/admin/oidc" {
		t.Fatal("test discovery")
	}
	if _, body := alice.get("/admin/oidc", "en"); !strings.Contains(body, "The provider answered") || !strings.Contains(body, "/login/oidc/balik") {
		t.Fatal("discovery test result / redirect URI")
	}
	if _, body := anon.get("/login", "en"); !strings.Contains(body, "Log in with Syarikat") {
		t.Fatal("the login page offers the provider")
	}

	// unknown email: refused, nothing created
	f.who = map[string]any{"sub": "s-bob", "email": "bob@company.example", "email_verified": true}
	nUsers := func() int { var n int; _ = h.st.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); return n }
	before := nUsers()
	if code, body, _ := oidcSignIn(t, h.browser(), f); code != 401 || !strings.Contains(body, "no active Codr account") || nUsers() != before {
		t.Fatalf("unknown email: %d", code)
	}
	// unverified email: refused even though it matches
	f.who = map[string]any{"sub": "s-alice", "email": "ALICE@company.example", "email_verified": false}
	if code, body, _ := oidcSignIn(t, h.browser(), f); code != 401 || !strings.Contains(body, "has not verified your email") {
		t.Fatalf("unverified: %d", code)
	}
	// wrong audience: refused
	f.who, f.aud = map[string]any{"sub": "s-alice", "email": "alice@company.example", "email_verified": true}, "lain"
	if code, body, _ := oidcSignIn(t, h.browser(), f); code != 401 || !strings.Contains(body, "failed its checks") {
		t.Fatalf("audience: %d", code)
	}
	f.aud = ""
	// the real thing: verified email matches alice (case-insensitive) → session, link written, audit says oidc
	b := h.browser()
	code, _, q := oidcSignIn(t, b, f)
	if code != 303 {
		t.Fatalf("sign-in: %d", code)
	}
	if c, body := b.get("/akaun", "en"); c != 200 || !strings.Contains(body, "alice@company.example") || !strings.Contains(body, "/akaun/oidc/padam") {
		t.Fatalf("signed in as alice with the link listed: %d", c)
	}
	if u, err := h.st.OIDCUser(ctx, f.srv.URL, "s-alice"); err != nil || u.Name != "alice" {
		t.Fatalf("link: %v %v", u.Name, err)
	}
	// the same callback again (replay) and a callback from a browser that did not start it are refused before the provider
	calls := f.tokenCalls
	if code, body := b.get("/login/oidc/balik?"+q, "en"); code != 401 || !strings.Contains(body, "expired or was started in another browser") {
		t.Fatalf("replay: %d", code)
	}
	other := h.browser()
	resp, _ := other.c.Get(h.srv.URL + "/login/oidc")
	resp.Body.Close()
	stolen, _ := url.Parse(resp.Header.Get("Location"))
	victim := h.browser()
	if code, _ := victim.get("/login/oidc/balik?code=x&state="+url.QueryEscape(stolen.Query().Get("state")), "en"); code != 401 || f.tokenCalls != calls {
		t.Fatalf("a callback in another browser: %d, provider called %d times", code, f.tokenCalls-calls)
	}
	// later the email changes at the provider: the linked subject still finds alice
	f.who = map[string]any{"sub": "s-alice", "email": "alice@baharu.my", "email_verified": true}
	if code, _, _ := oidcSignIn(t, h.browser(), f); code != 303 {
		t.Fatalf("linked subject: %d", code)
	}
	// 2FA on: the provider is a first factor only, the code page follows
	_, _ = h.st.DB().Exec(`UPDATE users SET totp_sealed = 'x', totp_on_at = ? WHERE id = ?`, store.Now(), adminU.ID)
	b2 := h.browser()
	resp2, _ := b2.c.Get(h.srv.URL + "/login/oidc")
	resp2.Body.Close()
	r3, _ := idpClient.Do(mustReq(resp2.Header.Get("Location")))
	r3.Body.Close()
	back, _ := url.Parse(r3.Header.Get("Location"))
	resp4, _ := b2.c.Get(h.srv.URL + "/login/oidc/balik?" + back.RawQuery)
	resp4.Body.Close()
	if resp4.StatusCode != 303 || !strings.HasPrefix(resp4.Header.Get("Location"), "/login/kod") {
		t.Fatalf("2FA after the provider: %d %s", resp4.StatusCode, resp4.Header.Get("Location"))
	}
	if c, _ := b2.get("/akaun", "en"); c == 200 {
		t.Fatal("a session exists before the second factor")
	}
	_, _ = h.st.DB().Exec(`UPDATE users SET totp_sealed = '', totp_on_at = NULL WHERE id = ?`, adminU.ID)
	// unlink: the link goes; a disabled account is refused even when linked
	b.post("/akaun/oidc/padam", url.Values{"issuer": {f.srv.URL}, "subject": {"s-alice"}})
	if _, err := h.st.OIDCUser(ctx, f.srv.URL, "s-alice"); err == nil {
		t.Fatal("unlink")
	}
	_, _ = h.st.DB().Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, store.Now(), adminU.ID)
	f.who = map[string]any{"sub": "s-alice", "email": "alice@company.example", "email_verified": true}
	if code, _, _ := oidcSignIn(t, h.browser(), f); code != 401 {
		t.Fatalf("disabled account: %d", code)
	}
	// refusals count as failed sign-ins for the same per-IP limit as passwords
	h.s.Limiter = &Limiter{Max: 1, Window: time.Minute}
	oidcSignIn(t, h.browser(), f)
	if code, _ := h.browser().get("/login/oidc", "en"); code != 429 {
		t.Fatalf("limit after a refusal: %d", code)
	}
}
