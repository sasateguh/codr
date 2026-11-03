package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var b64 = base64.RawURLEncoding.EncodeToString

func sign(t *testing.T, alg, kid string, key crypto.Signer, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	in := b64(h) + "." + b64(c)
	d := sha256.Sum256([]byte(in))
	var sig []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		sig, _ = rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	case *ecdsa.PrivateKey:
		r, s, _ := ecdsa.Sign(rand.Reader, k, d[:])
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	}
	return in + "." + b64(sig)
}

// TestVerify: a good RS256 and ES256 token pass; every claim and signature fault is refused, and so are "none" and HS256.
func TestVerify(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	epub, _ := ek.PublicKey.Bytes() // 0x04 || X || Y
	jwks := map[string]any{"keys": []map[string]string{
		{"kty": "RSA", "kid": "r1", "use": "sig", "n": b64(rk.N.Bytes()), "e": b64(big.NewInt(int64(rk.E)).Bytes())},
		{"kty": "EC", "kid": "e1", "crv": "P-256", "x": b64(epub[1:33]), "y": b64(epub[33:])},
		{"kty": "EC", "kid": "luar", "crv": "P-256", "x": b64(epub[1:33]), "y": b64(make([]byte, 32))}, // not on the curve
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(jwks) }))
	defer srv.Close()
	c := Client{HTTP: srv.Client(), Insecure: true}
	d := Discovery{Issuer: "https://idp.example", JWKSURI: srv.URL}
	now := time.Unix(1_800_000_000, 0)
	base := func() map[string]any {
		return map[string]any{"iss": "https://idp.example", "sub": "u-1", "aud": "codr", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
			"nonce": "n-1", "email": "a@x.my", "email_verified": true}
	}
	ctx := context.Background()
	if cl, err := c.Verify(ctx, d, sign(t, "RS256", "r1", rk, base()), "codr", "n-1", now, time.Minute); err != nil || cl.Subject != "u-1" || !cl.EmailVerified {
		t.Fatalf("good RS256: %+v %v", cl, err)
	}
	if _, err := c.Verify(ctx, d, sign(t, "ES256", "e1", ek, base()), "codr", "n-1", now, time.Minute); err != nil {
		t.Fatalf("good ES256: %v", err)
	}
	with := func(k string, v any) map[string]any { m := base(); m[k] = v; return m }
	without := func(k string) map[string]any { m := base(); delete(m, k); return m }
	bad := map[string]string{
		"iss":         sign(t, "RS256", "r1", rk, with("iss", "https://evil.example")),
		"aud":         sign(t, "RS256", "r1", rk, with("aud", "other")),
		"aud list":    sign(t, "RS256", "r1", rk, with("aud", []string{"other", "codr"})), // several audiences need azp = us
		"expired":     sign(t, "RS256", "r1", rk, with("exp", now.Add(-2*time.Minute).Unix())),
		"future":      sign(t, "RS256", "r1", rk, with("iat", now.Add(10*time.Minute).Unix())),
		"nonce":       sign(t, "RS256", "r1", rk, with("nonce", "n-2")),
		"no nonce":    sign(t, "RS256", "r1", rk, without("nonce")),
		"no sub":      sign(t, "RS256", "r1", rk, without("sub")),
		"unknown kid": sign(t, "RS256", "zz", rk, base()),
		"wrong key":   sign(t, "ES256", "r1", ek, base()),
		"off curve":   sign(t, "ES256", "luar", ek, base()),
		"none":        b64([]byte(`{"alg":"none"}`)) + "." + b64(mustJSON(base())) + ".",
		"HS256":       b64([]byte(`{"alg":"HS256","kid":"r1"}`)) + "." + b64(mustJSON(base())) + "." + b64([]byte("x")),
		"not a JWS":   "abc",
	}
	good := sign(t, "RS256", "r1", rk, base())
	p := strings.Split(good, ".")
	tampered := mustJSON(with("sub", "admin"))
	bad["tampered"] = p[0] + "." + b64(tampered) + "." + p[2]
	for name, tok := range bad {
		if _, err := c.Verify(ctx, d, tok, "codr", "n-1", now, time.Minute); !errors.Is(err, ErrToken) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	m := with("aud", []string{"other", "codr"})
	m["azp"] = "codr"
	if _, err := c.Verify(ctx, d, sign(t, "RS256", "r1", rk, m), "codr", "n-1", now, time.Minute); err != nil {
		t.Fatalf("several audiences with azp: %v", err)
	}
	if cl, _ := c.Verify(ctx, d, sign(t, "RS256", "r1", rk, with("email_verified", "true")), "codr", "n-1", now, time.Minute); !cl.EmailVerified {
		t.Fatal("email_verified as a string")
	}
	if cl, _ := c.Verify(ctx, d, sign(t, "RS256", "r1", rk, with("email_verified", false)), "codr", "n-1", now, time.Minute); cl.EmailVerified {
		t.Fatal("unverified email reported verified")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestDiscoverAndPKCE(t *testing.T) {
	var issuer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": issuer, "authorization_endpoint": "https://idp/a", "token_endpoint": "https://idp/t", "jwks_uri": "https://idp/k"})
	}))
	defer srv.Close()
	c := Client{HTTP: srv.Client(), Insecure: true}
	issuer = srv.URL
	if _, err := c.Discover(context.Background(), srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	issuer = "https://evil.example"
	if _, err := c.Discover(context.Background(), srv.URL); !errors.Is(err, ErrBadIssuer) {
		t.Fatalf("issuer mismatch: %v", err)
	}
	if _, err := (Client{HTTP: srv.Client()}).Discover(context.Background(), srv.URL); err == nil {
		t.Fatal("http issuer accepted without Insecure")
	}
	// RFC 7636 appendix B
	if got := Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Fatalf("challenge: %s", got)
	}
	u := AuthURL(Discovery{AuthorizationEndpoint: "https://idp/a?x=1"}, "codr", "https://f/cb", "openid email", "s", "n", "v")
	if !strings.HasPrefix(u, "https://idp/a?x=1&") || !strings.Contains(u, "code_challenge_method=S256") || !strings.Contains(u, "nonce=n") {
		t.Fatalf("auth url: %s", u)
	}
}
