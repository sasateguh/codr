// Package webhook delivers repository events to outside URLs, signed with HMAC-SHA256 (D78).
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
)

// Errors a form maps to messages.
var (
	ErrBadURL  = errors.New("webhook: URL must be http(s)://host/... without user or password")
	ErrPrivate = errors.New("webhook: address is private, loopback or link-local")
)

// Policy decides which addresses a webhook may reach. Anyone who administers a repository can add a webhook, so
// without it a webhook is a way to make this server call its own neighbours (the LLM router, the database, cloud
// metadata). The check runs on the address actually dialled, after DNS, so a name that resolves to 127.0.0.1 or
// changes its answer between check and dial is still refused.
type Policy struct {
	AllowPrivate bool     // setting webhook.allow_private
	AllowHosts   []string // setting webhook.allow_hosts: host names that may resolve to private addresses
}

// ParseHosts reads the comma list of webhook.allow_hosts.
func ParseHosts(v string) []string {
	var out []string
	for _, h := range strings.Split(v, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func (p Policy) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, h := range p.AllowHosts {
		if h == host {
			return true
		}
	}
	return false
}

// CheckURL validates a webhook URL before it is saved: scheme, host, no credentials, and a literal IP must pass the
// address rule. Names are checked again at every dial.
func (p Policy) CheckURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2000 {
		return nil, ErrBadURL
	}
	host := u.Hostname()
	if host == "" {
		return nil, ErrBadURL
	}
	if ip, err := netip.ParseAddr(host); err == nil && !p.AllowPrivate && !p.hostAllowed(host) && blocked(ip) {
		return nil, ErrPrivate
	}
	if !p.AllowPrivate && !p.hostAllowed(host) && (host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".internal")) {
		return nil, ErrPrivate
	}
	return u, nil
}

// cgnat and other ranges netip has no predicate for.
var extraBlocked = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT, often a cloud's internal network
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() ||
		ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() || !ip.IsValid() {
		return true
	}
	for _, p := range extraBlocked {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// dialer returns a DialContext that refuses blocked addresses at connect time. host is the URL's host name, which
// the allow list is matched against.
func (p Policy) dialer(base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		d := *base
		if !p.AllowPrivate && !p.hostAllowed(host) {
			d.Control = func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil || blocked(ap.Addr()) {
					return ErrPrivate
				}
				return nil
			}
		}
		return d.DialContext(ctx, network, addr)
	}
}

// Sign is the value of X-Codr-Signature-256 (and X-Hub-Signature-256): "sha256=" + hex HMAC of the exact body.
func Sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature header in constant time (for receivers written in Go, and for the tests).
func Verify(secret string, body []byte, header string) bool {
	return hmac.Equal([]byte(Sign(secret, body)), []byte(header))
}
