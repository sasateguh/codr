package web

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"codr.my/codr/internal/store"
)

var errBadCredentials = errors.New("web: bad credentials")

// authenticate reads Authorization. No header = anonymous (nil, nil, nil). Basic carries
// user:token (git's form) and the user name must match the token's owner; Bearer carries the
// token alone. Anything else, or an unknown/expired token, is an error.
func (s *Server) authenticate(ctx context.Context, r *http.Request) (*store.AccessToken, *store.User, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, nil, nil
	}
	scheme, rest, _ := strings.Cut(h, " ")
	var wantUser, raw string
	switch strings.ToLower(scheme) {
	case "basic":
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return nil, nil, errBadCredentials
		}
		wantUser, raw, _ = strings.Cut(string(dec), ":")
	case "bearer":
		raw = strings.TrimSpace(rest)
	default:
		return nil, nil, errBadCredentials
	}
	if !strings.HasPrefix(raw, store.TokenPrefix) && !strings.HasPrefix(raw, store.LegacyTokenPrefix) {
		return nil, nil, errBadCredentials // a password, or nothing: tokens only (D8)
	}
	tok, u, err := s.Store.TokenUser(ctx, raw)
	if err != nil {
		return nil, nil, errBadCredentials
	}
	if wantUser != "" && !strings.EqualFold(wantUser, u.Name) {
		return nil, nil, errBadCredentials
	}
	return &tok, &u, nil
}

// Limiter blocks an IP after Max failed attempts inside Window (fixed window, in memory).
type Limiter struct {
	Max    int
	Window time.Duration

	mu   sync.Mutex
	hits map[string]hit
}

type hit struct {
	n     int
	since time.Time
}

// Fail records one failure.
func (l *Limiter) Fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string]hit{}
	}
	now := time.Now()
	h := l.hits[ip]
	if now.Sub(h.since) > l.Window {
		h = hit{since: now}
	}
	h.n++
	l.hits[ip] = h
	if len(l.hits) > 10000 { // bound memory: drop everything stale
		for k, v := range l.hits {
			if now.Sub(v.since) > l.Window {
				delete(l.hits, k)
			}
		}
	}
}

// Blocked reports whether ip has exhausted its attempts in the current window.
func (l *Limiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.hits[ip]
	if !ok || time.Since(h.since) > l.Window {
		return false
	}
	return h.n >= l.Max
}
