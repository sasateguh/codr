// Package oidc is a small OpenID Connect relying party (D91): discovery, the authorization code flow with PKCE, and
// ID token verification (RS256 / ES256 against the provider's JWKS). Only what sign-in needs; no library.
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
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Discovery is the part of /.well-known/openid-configuration we use.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	Algs                  []string `json:"id_token_signing_alg_values_supported"`
}

// Claims are the ID token claims sign-in reads.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// Errors a caller may want to tell apart (they are all "sign-in refused" to a person).
var (
	ErrBadIssuer = errors.New("oidc: issuer mismatch")
	ErrToken     = errors.New("oidc: invalid id token")
)

const maxBody = 1 << 20

// Client talks to one provider.
type Client struct {
	HTTP *http.Client
	// Insecure allows http:// issuers and endpoints (tests and a provider on a private network); off by default.
	Insecure bool
}

func (c Client) okURL(u string) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || (p.Scheme != "https" && !(c.Insecure && p.Scheme == "http")) {
		return fmt.Errorf("oidc: %q is not an https URL", u)
	}
	return nil
}

func (c Client) getJSON(ctx context.Context, u string, v any) error {
	if err := c.okURL(u); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("oidc: GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v)
}

// Discover reads the provider's configuration and checks that it names the same issuer (OIDC Discovery §4.3).
func (c Client) Discover(ctx context.Context, issuer string) (Discovery, error) {
	var d Discovery
	issuer = strings.TrimRight(issuer, "/")
	if err := c.getJSON(ctx, issuer+"/.well-known/openid-configuration", &d); err != nil {
		return d, err
	}
	if strings.TrimRight(d.Issuer, "/") != issuer {
		return d, fmt.Errorf("%w: configured %q, provider says %q", ErrBadIssuer, issuer, d.Issuer)
	}
	for _, u := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI} {
		if err := c.okURL(u); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Random returns n random bytes, base64url without padding (state, nonce, PKCE verifier).
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Challenge is the S256 PKCE challenge of a verifier (RFC 7636 §4.2).
func Challenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// AuthURL is where the browser goes to sign in at the provider.
func AuthURL(d Discovery, clientID, redirect, scopes, state, nonce, verifier string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "scope": {scopes},
		"state": {state}, "nonce": {nonce}, "code_challenge": {Challenge(verifier)}, "code_challenge_method": {"S256"}}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + q.Encode()
}

// Exchange trades the code for an ID token at the token endpoint (client_secret_basic, with the PKCE verifier).
func (c Client) Exchange(ctx context.Context, d Discovery, clientID, secret, redirect, code, verifier string) (string, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "client_id": {clientID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil {
		return "", fmt.Errorf("oidc: token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.IDToken == "" {
		return "", fmt.Errorf("oidc: token endpoint: HTTP %d %s", resp.StatusCode, out.Error)
	}
	return out.IDToken, nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) public() (crypto.PublicKey, error) {
	dec := base64.RawURLEncoding.DecodeString
	switch k.Kty {
	case "RSA":
		n, err1 := dec(k.N)
		e, err2 := dec(k.E)
		if err1 != nil || err2 != nil || len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("oidc: bad RSA key (at least 2048 bits)")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, errors.New("oidc: only P-256 EC keys")
		}
		x, err1 := dec(k.X)
		y, err2 := dec(k.Y)
		if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
			return nil, errors.New("oidc: bad EC key")
		}
		// the parser rejects a point that is not on the curve
		pk, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, errors.New("oidc: bad EC key: " + err.Error())
		}
		return pk, nil
	}
	return nil, errors.New("oidc: unsupported key type " + k.Kty)
}

// Verify checks an ID token: signature (RS256 or ES256, key from the provider's JWKS by kid), issuer, audience,
// authorised party, expiry, issue time and nonce. now and skew come from the caller (tests pin the clock).
func (c Client) Verify(ctx context.Context, d Discovery, raw, clientID, nonce string, now time.Time, skew time.Duration) (Claims, error) {
	var cl Claims
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return cl, fmt.Errorf("%w: not a JWS", ErrToken)
	}
	hb, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	pb, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return cl, fmt.Errorf("%w: encoding", ErrToken)
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &h) != nil || (h.Alg != "RS256" && h.Alg != "ES256") {
		return cl, fmt.Errorf("%w: algorithm %q not accepted", ErrToken, h.Alg) // never "none", never HMAC with a public key
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := c.getJSON(ctx, d.JWKSURI, &set); err != nil {
		return cl, err
	}
	var key crypto.PublicKey
	for _, k := range set.Keys {
		if (h.Kid == "" || k.Kid == h.Kid) && (k.Use == "" || k.Use == "sig") && (k.Alg == "" || k.Alg == h.Alg) {
			if pk, err := k.public(); err == nil {
				key = pk
				break
			}
		}
	}
	if key == nil {
		return cl, fmt.Errorf("%w: no key %q in the provider's JWKS", ErrToken, h.Kid)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch h.Alg {
	case "RS256":
		pk, ok := key.(*rsa.PublicKey)
		if !ok || rsa.VerifyPKCS1v15(pk, crypto.SHA256, digest[:], sig) != nil {
			return cl, fmt.Errorf("%w: signature", ErrToken)
		}
	case "ES256":
		pk, ok := key.(*ecdsa.PublicKey)
		if !ok || len(sig) != 64 || !ecdsa.Verify(pk, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return cl, fmt.Errorf("%w: signature", ErrToken)
		}
	}
	var p struct {
		Iss           string          `json:"iss"`
		Sub           string          `json:"sub"`
		Aud           json.RawMessage `json:"aud"`
		Azp           string          `json:"azp"`
		Exp           json.Number     `json:"exp"`
		Iat           json.Number     `json:"iat"`
		Nonce         string          `json:"nonce"`
		Email         string          `json:"email"`
		EmailVerified any             `json:"email_verified"`
		Name          string          `json:"name"`
	}
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return cl, fmt.Errorf("%w: claims", ErrToken)
	}
	var auds []string
	if json.Unmarshal(p.Aud, &auds) != nil {
		var one string
		if json.Unmarshal(p.Aud, &one) != nil {
			return cl, fmt.Errorf("%w: aud", ErrToken)
		}
		auds = []string{one}
	}
	hasAud := false
	for _, a := range auds {
		hasAud = hasAud || a == clientID
	}
	exp, errE := p.Exp.Int64()
	iat, errI := p.Iat.Int64()
	switch {
	case strings.TrimRight(p.Iss, "/") != strings.TrimRight(d.Issuer, "/"):
		return cl, fmt.Errorf("%w: iss", ErrToken)
	case !hasAud:
		return cl, fmt.Errorf("%w: aud", ErrToken)
	case len(auds) > 1 && p.Azp != clientID:
		return cl, fmt.Errorf("%w: azp", ErrToken)
	case errE != nil || now.After(time.Unix(exp, 0).Add(skew)):
		return cl, fmt.Errorf("%w: expired", ErrToken)
	case errI != nil || time.Unix(iat, 0).After(now.Add(skew)):
		return cl, fmt.Errorf("%w: issued in the future", ErrToken)
	case nonce == "" || p.Nonce != nonce:
		return cl, fmt.Errorf("%w: nonce", ErrToken)
	case p.Sub == "":
		return cl, fmt.Errorf("%w: sub", ErrToken)
	}
	cl = Claims{Subject: p.Sub, Email: strings.TrimSpace(p.Email), Name: p.Name}
	switch v := p.EmailVerified.(type) {
	case bool:
		cl.EmailVerified = v
	case string:
		cl.EmailVerified = v == "true" // some providers send it as a string
	}
	return cl, nil
}
