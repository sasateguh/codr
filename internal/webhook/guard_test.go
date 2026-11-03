package webhook

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestBlockedAddresses(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "127.9.9.9", "::1", "10.0.1.1", "172.16.0.5", "192.168.1.1", "169.254.169.254", "fe80::1",
		"fc00::1", "100.64.0.1", "0.0.0.0", "::", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "224.0.0.1", "198.18.0.1"} {
		if !blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s must be blocked", a)
		}
	}
	for _, a := range []string{"1.1.1.1", "8.8.8.8", "104.16.0.1", "2606:4700::1111"} {
		if blocked(netip.MustParseAddr(a)) {
			t.Errorf("%s must be allowed", a)
		}
	}
}

func TestCheckURL(t *testing.T) {
	p := Policy{}
	for raw, want := range map[string]error{
		"https://example.com/hook":         nil,
		"http://example.com:8080/x?y=1":    nil,
		"ftp://example.com/":               ErrBadURL,
		"https://user:pw@example.com/":     ErrBadURL,
		"https:///nohost":                  ErrBadURL,
		"example.com/hook":                 ErrBadURL,
		"https://example.com/#frag":        ErrBadURL,
		"http://127.0.0.1:3000/":           ErrPrivate, // Forgejo on this host
		"http://10.0.1.1:8080/v1":          ErrPrivate, // a gateway on the private network
		"http://169.254.169.254/latest/":   ErrPrivate, // cloud metadata
		"http://[::1]/":                    ErrPrivate,
		"http://localhost:9/":              ErrPrivate,
		"http://db.internal/":              ErrPrivate,
		"http://[::ffff:127.0.0.1]:3100/x": ErrPrivate,
	} {
		_, err := p.CheckURL(raw)
		if !errors.Is(err, want) && !(want == nil && err == nil) {
			t.Errorf("%s: got %v want %v", raw, err, want)
		}
	}
	if _, err := (Policy{AllowPrivate: true}).CheckURL("http://127.0.0.1:3000/"); err != nil {
		t.Errorf("allow_private: %v", err)
	}
	if _, err := (Policy{AllowHosts: []string{"ci.local"}}).CheckURL("http://ci.local/hook"); err != nil {
		t.Errorf("allow_hosts: %v", err)
	}
}

// The dial-time check is the one that matters: a public-looking name that resolves to a private address is refused
// when connecting, not only when the URL is saved.
func TestDialRefusesPrivateAfterResolution(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	client := func(p Policy) *http.Client {
		return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: p.dialer(&net.Dialer{Timeout: 5 * time.Second})}}
	}
	// "localhost" resolves to loopback: blocked at the socket even though the dial was by name
	_, err := client(Policy{}).Get("http://localhost:" + port + "/")
	if !errors.Is(err, ErrPrivate) {
		t.Fatalf("dial to loopback by name: %v", err)
	}
	if resp, err := client(Policy{AllowHosts: []string{"localhost"}}).Get("http://localhost:" + port + "/"); err != nil {
		t.Fatalf("allow-listed host: %v", err)
	} else {
		resp.Body.Close()
	}
	if resp, err := client(Policy{AllowPrivate: true}).Get(srv.URL); err != nil {
		t.Fatalf("allow_private: %v", err)
	} else {
		resp.Body.Close()
	}
}

func TestSignVerify(t *testing.T) {
	body := []byte(`{"event":"ping"}`)
	sig := Sign("whsec_abc", body)
	// independent value: printf '{"event":"ping"}' | openssl dgst -sha256 -hmac whsec_abc
	if sig != "sha256=63abae981c11ef40efbb5cb46f506d34af90ef291d7110b1f56adeb81e1e61ab" {
		t.Fatalf("shape: %s", sig)
	}
	if !Verify("whsec_abc", body, sig) || Verify("other", body, sig) || Verify("whsec_abc", []byte(`{}`), sig) {
		t.Fatal("verify")
	}
}

func TestValidKinds(t *testing.T) {
	got := ValidKinds([]string{"pull_request", "bogus", "push", "push"})
	if len(got) != 2 || got[0] != "push" || got[1] != "pull_request" {
		t.Fatalf("%v", got)
	}
}
