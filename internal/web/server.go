// Package web is the HTTPS door of Codr: /healthz and smart HTTP git under
// /<owner>/<repo>.git. Authentication is token-only (D8, hashed D28); permission is the
// same store.AccessFor decision as the SSH door, taken before git is started. The web UI
// (v0.3) mounts beside these routes.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"codr.my/codr/internal/cloud"
	"codr.my/codr/internal/gitcmd"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/lfs"
	"codr.my/codr/internal/mirror"
	"codr.my/codr/internal/names"
	"codr.my/codr/internal/notify"
	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/version"
	"codr.my/codr/internal/webhook"
	"codr.my/codr/internal/xref"
)

// Server serves the HTTP door.
type Server struct {
	Store *store.Store
	Disk  repos.Disk
	Git   gitcmd.Runner
	// MaxInputBytes bounds a push body (receive.maxInputSize and the request body), 0 = none.
	MaxInputBytes int64
	// DefaultLang is used before the settings table answers (config site.default_language).
	DefaultLang string
	// Limiter counts failed token attempts per client IP; nil = unlimited.
	Limiter *Limiter
	// TrustedProxies are peers allowed to name the real client in ClientIPHeader (D44).
	// Empty = the TCP peer is the client.
	TrustedProxies []netip.Prefix
	ClientIPHeader string
	// EdgeRanges: when a trusted proxy sends X-Forwarded-For, its last hop must be inside these for
	// ClientIPHeader to count (config server.edge_ranges, D58).
	EdgeRanges []netip.Prefix
	Log        *slog.Logger
	// UI configures the HTML pages (v0.3).
	UI UI
	// Mirror runs imports started from the web (nil = the create form offers no URL field).
	Mirror *mirror.Syncer
	// LandingHosts get the product landing page instead of the forge (config site.landing_hosts, D56).
	LandingHosts []string
	// RedirectHosts maps an old host name to its new one (config site.redirect_hosts, D58).
	RedirectHosts map[string]string
	// GitHubAPI is the REST base for the issue/PR import (D72); "" = https://api.github.com.
	GitHubAPI string
	// Cloud runs backups to the owner's bucket (D70); nil = the admin page says so.
	Cloud *cloud.Job
	// Webhooks is woken after an event is written (nil = events wait for the next tick).
	Webhooks *webhook.Dispatcher
	// Notifier is woken after an event is written, like Webhooks (D79).
	Notifier *notify.Notifier
	// Closer is woken after an event is written: pushes and merges may close issues (D87).
	Closer *xref.Closer
	// OIDCInsecure lets an OpenID Connect provider use http:// (tests); production providers are https only (D91).
	OIDCInsecure bool
	// LFS stores Git LFS objects (D82); nil = the LFS endpoints answer "not enabled".
	LFS *lfs.Store
}

// Handler builds the router: git transport, then the HTML UI beside it. Init must have run.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", s.gitRoute)
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", s.gitRoute)
	mux.HandleFunc("POST /{owner}/{repo}/git-receive-pack", s.gitRoute)
	s.registerLFS(mux) // Git LFS (D82), beside the git routes
	s.registerAPI(mux) // REST API v1 (D81), tokens only
	s.mountUI(mux)
	static := s.staticHandler()
	return secureHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if to, ok := s.RedirectHosts[requestHost(r)]; ok {
			code := http.StatusMovedPermanently
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				code = http.StatusPermanentRedirect // keep the method: a git push or a form post must not turn into a GET
			}
			http.Redirect(w, r, s.scheme()+"://"+to+r.URL.RequestURI(), code)
			return
		}
		if host, ok := s.landingHost(r); ok {
			s.landing(w, r, host)
			return
		}
		// the forge's own index switch (D66): noindex unless site.index, on every response including raw files
		indexed := s.forgeIndexed(r.Context())
		if !indexed {
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
		if r.URL.Path == "/robots.txt" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			if indexed {
				_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
			} else {
				_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/static/") {
			static.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

// forgeIndexed reports the site.index setting; anything unreadable counts as "do not index".
func (s *Server) forgeIndexed(ctx context.Context) bool {
	v, err := s.Store.SettingBool(ctx, "site.index")
	return err == nil && v
}

// Init parses templates and fingerprints static files. Call once before Handler.
func (s *Server) Init() error { return s.initUI() }

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// no-transform: behind Cloudflare a browser-shaped request (Accept text/html + gzip) otherwise gets the
		// Web Analytics beacon injected into every page — blocked by our CSP, but a console error on each load
		// (measured 14 Oct 2026 through forge.amcd.my). Cloudflare honours no-transform and leaves the body alone.
		h.Set("Cache-Control", "no-store, no-transform")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	v, err := s.Store.Version(ctx)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "version": version.String(), "schema": v})
}

// lang picks the response language: the authenticated user's, else Accept-Language,
// else the site default.
func (s *Server) lang(r *http.Request, u *store.User) string {
	if u != nil {
		return i18n.Normalize(u.Lang, s.DefaultLang)
	}
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if l := i18n.Normalize(tag, ""); l != "" {
			return l
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if def, err := s.Store.Setting(ctx, "site.default_language"); err == nil {
		return i18n.Normalize(def, s.DefaultLang)
	}
	return s.DefaultLang
}

func (s *Server) text(w http.ResponseWriter, r *http.Request, code int, key string, args ...any) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(i18n.T(s.lang(r, nil), key, args...) + "\n"))
}

// clientIP is the TCP peer, unless that peer is a trusted proxy (the cloudflared container
// of a Cloudflare Tunnel, D44): then ClientIPHeader names the real client. An untrusted peer
// cannot spoof a client by sending the header; a trusted peer that omits it, or sends
// garbage, is counted as itself.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(s.TrustedProxies) == 0 {
		return host
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	trusted := false
	for _, p := range s.TrustedProxies {
		if p.Contains(peer) {
			trusted = true
			break
		}
	}
	if !trusted {
		return host
	}
	// A proxy that forwards X-Forwarded-For (traefik) names the address IT saw last. Only when that hop is a
	// CDN edge may the edge's header be believed; someone who reaches the proxy directly is that hop (D58).
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && len(s.EdgeRanges) > 0 {
		parts := strings.Split(xff, ",")
		hop, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1]))
		if err != nil {
			return host
		}
		hop = hop.Unmap()
		edge := false
		for _, p := range s.EdgeRanges {
			if p.Contains(hop) {
				edge = true
				break
			}
		}
		if !edge {
			return hop.String()
		}
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get(s.ClientIPHeader))); err == nil {
		return a.Unmap().String()
	}
	return host
}

// gitRoute authorises and runs one smart-HTTP request.
func (s *Server) gitRoute(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	name, ok := strings.CutSuffix(r.PathValue("repo"), ".git")
	if !ok || names.User(owner) != nil || names.Repo(name) != nil {
		s.text(w, r, http.StatusNotFound, "http.not_found", owner+"/"+r.PathValue("repo"))
		return
	}
	ref := owner + "/" + name
	svc, ok := s.service(r)
	if !ok {
		s.text(w, r, http.StatusNotFound, "http.no_page")
		return
	}
	ip := s.clientIP(r)
	if s.Limiter != nil && s.Limiter.Blocked(ip) {
		s.text(w, r, http.StatusTooManyRequests, "http.too_many")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tok, user, authErr := s.authenticate(ctx, r)
	if authErr != nil {
		if s.Limiter != nil {
			s.Limiter.Fail(ip)
		}
		s.Log.Warn("http auth failed", "ip", ip, "repo", ref)
		s.unauthorized(w, r)
		return
	}
	var uid int64
	var lang string
	if user != nil {
		uid, lang = user.ID, i18n.Normalize(user.Lang, s.DefaultLang)
	} else {
		lang = s.lang(r, nil)
	}
	need := store.AccessRead
	if svc == gitcmd.ReceivePack {
		need = store.AccessWrite
	}
	repo, err := s.Store.RepoByPath(ctx, owner, name)
	isWiki := false
	if errors.Is(err, store.ErrNotFound) { // owner/name.wiki.git = the wiki of owner/name (D83), unless a repository has that name
		if base, ok := strings.CutSuffix(name, ".wiki"); ok {
			if br, berr := s.Store.RepoByPath(ctx, owner, base); berr == nil {
				repo, err, isWiki = br, nil, true
			}
		}
	}
	access := store.AccessNone
	if err == nil {
		access, err = s.Store.AccessFor(ctx, uid, repo)
	}
	switch {
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.Log.Error("http access lookup", "err", err)
		s.text(w, r, http.StatusInternalServerError, "http.internal")
		return
	case access < need && user == nil:
		// Anonymous and not allowed: ask for a token. Unknown and private repos answer the
		// same way so existence does not leak (D29 at this door).
		s.unauthorized(w, r)
		return
	case access < store.AccessRead:
		s.plain(w, http.StatusNotFound, lang, "http.not_found", ref)
		return
	case access < need:
		s.plain(w, http.StatusForbidden, lang, "http.write_denied", ref)
		return
	case need == store.AccessWrite && repo.Archived:
		s.plain(w, http.StatusForbidden, lang, "http.archived", ref)
		return
	case need == store.AccessWrite && repo.Mirror:
		s.plain(w, http.StatusForbidden, lang, "http.mirror", ref)
		return
	case tok != nil && !tok.Allows(need):
		s.plain(w, http.StatusForbidden, lang, "http.token_scope", scopeFor(need))
		return
	}
	if isWiki {
		if svc == gitcmd.ReceivePack {
			s.plain(w, http.StatusForbidden, lang, "http.wiki_readonly", ref)
			return
		}
		if fi, err := os.Stat(s.Disk.WikiPath(repo.ID)); err != nil || !fi.IsDir() {
			s.plain(w, http.StatusNotFound, lang, "http.not_found", ref)
			return
		}
	} else if !s.Disk.Exists(repo.ID) {
		s.Log.Error("repo missing on disk", "repo", ref, "path", s.Disk.Path(repo.ID))
		s.plain(w, http.StatusInternalServerError, lang, "http.internal")
		return
	}
	push := gitcmd.Push{RepoID: repo.ID, UserID: uid, Lang: lang, Door: "https", MaxInputBytes: s.MaxInputBytes}
	if svc == gitcmd.ReceivePack {
		if push.Fsck, err = s.Store.SettingBool(ctx, "repo.receive_fsck"); err != nil {
			s.Log.Error("http setting lookup", "err", err)
			s.plain(w, http.StatusInternalServerError, lang, "http.internal")
			return
		}
		if s.MaxInputBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, s.MaxInputBytes)
		}
		if tok != nil {
			_ = s.Store.TouchToken(ctx, tok.ID)
		}
	}
	remoteUser := ""
	if user != nil {
		remoteUser = user.Name
	}
	s.backend(w, r, repo.ID, isWiki, remoteUser, push)
}

// service maps the request to the git verb it will run, or false for anything else.
func (s *Server) service(r *http.Request) (gitcmd.Service, bool) {
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/info/refs"):
		return gitcmd.Parse(r.URL.Query().Get("service"))
	case strings.HasSuffix(p, "/git-upload-pack"):
		return gitcmd.UploadPack, true
	case strings.HasSuffix(p, "/git-receive-pack"):
		return gitcmd.ReceivePack, true
	}
	return "", false
}

func scopeFor(a store.Access) string {
	if a >= store.AccessWrite {
		return store.ScopeWrite
	}
	return store.ScopeRead
}

func (s *Server) unauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Codr", charset="UTF-8"`)
	s.text(w, r, http.StatusUnauthorized, "http.unauthorized")
}

func (s *Server) plain(w http.ResponseWriter, code int, lang, key string, args ...any) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(i18n.T(lang, key, args...) + "\n"))
}
