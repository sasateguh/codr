package web

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/gitread"
	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/store"
	"codr.my/codr/internal/version"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	cookieSession = "codr_sesi"
	cookieCSRF    = "codr_csrf"
	cookieLang    = "codr_lang"
)

// UI holds what the HTML pages need beyond the git door.
type UI struct {
	SessionTTL     time.Duration
	Argon          auth.Params
	PasswordMinLen int // shortest password /akaun accepts (config auth.password_min_len)
	SecureCookies  bool
	SSHHost        string
	HTTPHost       string
	GitTimeout     time.Duration

	tmpl       *template.Template
	staticHash string
	dummyHash  string // a real hash of a random secret: failed logins for unknown users cost the same
}

// page is the data every template receives as $.
type page struct {
	Lang      string
	User      *store.User
	CSRF      string
	SiteName  string
	Static    string
	Version   string
	Path      string
	Title     string
	Now       time.Time
	Flash     string
	FlashKind string
	OGImage   string // absolute URL of the share card (og.png), from http_host
	OtherLang string // the other UI language, for the bilingual brand line
	Indexed   bool   // site.index: false = <meta name="robots" content="noindex"> (D66)
	Unread    int    // unread notifications, for the bell (D79)
	Data      any
}

// T translates in the page's language.
func (p page) T(key string, args ...any) string { return i18n.T(p.Lang, key, args...) }

// N translates a counted message, choosing the "<key>.one" form for exactly one (i18n.N).
func (p page) N(key string, n int, args ...any) string { return i18n.N(p.Lang, key, n, args...) }

// TIn translates in a given language (the login hero shows the tagline in both).
func (p page) TIn(lang, key string, args ...any) string { return i18n.T(lang, key, args...) }

// Ago formats a relative time.
func (p page) Ago(t time.Time) string { return relTime(p.Lang, t, p.Now) }

// staticGeneration is bumped when the way static files are delivered changes without their content changing.
const staticGeneration = "penghantaran-2\n"

func (s *Server) initUI() error {
	funcs := template.FuncMap{
		"size":      humanSize,
		"short":     func(h string) string { return gitread.Commit{Hash: h}.Short() },
		"join":      path.Join,
		"joinList":  func(xs []string) string { return strings.Join(xs, ", ") },
		"crumbs":    crumbs,
		"add":       func(a, b int) int { return a + b },
		"sub":       func(a, b int) int { return a - b },
		"lines":     func(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") },
		"rfc":       func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
		"day":       func(t time.Time) string { return t.UTC().Format("2006-01-02") },
		"hasSuffix": func(s, suf string) bool { return strings.HasSuffix(strings.ToLower(s), suf) },
		"parseTime": func(s string) time.Time { t, _ := time.Parse("2006-01-02T15:04:05.000Z", s); return t },
		"hue":       hueClass,
		"hlDiff":    highlightDiff,
		"splitDiff": splitDiff,
		"notesAt":   notesAt,
		"askHref":   askHref,
		"initial":   initialOf,
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				if k, ok := kv[i].(string); ok {
					m[k] = kv[i+1]
				}
			}
			return m
		},
	}
	t, err := template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	s.UI.tmpl = t
	css, err := fs.ReadFile(staticFS, "static/codr.css")
	if err != nil {
		return err
	}
	// the delivery generation joins the hash: when HOW assets are delivered changes (D94: compression moved to traefik)
	// but their bytes do not, a cache keyed by URL + ETag (Cloudflare revalidates with 304) would keep the old body forever
	css = append([]byte(staticGeneration), css...)
	// every asset the pages reference by this hash is part of it: a changed logo or tema.js must bust the URL too
	for _, name := range []string{"static/codr.js", "static/tema.js", "static/logo.svg", "static/og.png", "static/apple-touch-icon.png", "static/pendaratan.css", "static/pendaratan.js", "static/produk/pr-cerah-ms.webp", "static/produk/pr-gelap-ms.webp", "static/produk/fail-cerah-ms.webp", "static/produk/fail-gelap-ms.webp",
		"static/produk/pr-cerah-en.webp", "static/produk/pr-gelap-en.webp", "static/produk/fail-cerah-en.webp", "static/produk/fail-gelap-en.webp"} {
		b, err := fs.ReadFile(staticFS, name)
		if err != nil {
			return err
		}
		css = append(css, b...)
	}
	sum := sha256.Sum256(css)
	s.UI.staticHash = hex.EncodeToString(sum[:4])
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	if s.UI.dummyHash, err = auth.Hash(hex.EncodeToString(b), s.UI.Argon); err != nil {
		return err
	}
	return nil
}

// mountUI adds the HTML routes beside the git ones. Static files are served by staticHandler
// before the mux: a "/static/{hash}/" pattern would conflict with "/{owner}/{repo}/…" in
// ServeMux's specificity rules, and "static" is a reserved user name anyway.
func (s *Server) mountUI(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", s.uiHome)
	mux.HandleFunc("GET /login", s.uiLoginForm)
	mux.HandleFunc("POST /login", s.uiLogin)
	mux.HandleFunc("POST /logout", s.uiLogout)
	mux.HandleFunc("GET /login/kod", s.uiLoginCodeForm)
	mux.HandleFunc("POST /login/kod", s.uiLoginCode)
	mux.HandleFunc("GET /akaun/2fa", s.ui2FASetup)
	mux.HandleFunc("POST /akaun/2fa", s.ui2FAConfirm)
	mux.HandleFunc("POST /akaun/2fa/matikan", s.ui2FADisable)
	mux.HandleFunc("POST /akaun/2fa/pemulihan", s.ui2FACodes)
	mux.HandleFunc("POST /akaun/sesi/satu", s.uiSessionEnd) // literal: /akaun/sesi/{x} would clash with /{owner}/{repo}/git-upload-pack
	mux.HandleFunc("POST /akaun/sesi/lain", s.uiSessionEnd)
	mux.HandleFunc("POST /lang", s.uiLang)
	mux.HandleFunc("GET /api/cari", s.uiPalette)
	mux.HandleFunc("GET /baharu", s.uiNewForm)
	mux.HandleFunc("POST /baharu", s.uiNewCreate)
	mux.HandleFunc("GET /akaun", s.uiAccount)
	mux.HandleFunc("POST /akaun/kunci", s.uiAccountKeyAdd)
	mux.HandleFunc("POST /akaun/kunci/padam", s.uiAccountKeyRemove)
	mux.HandleFunc("POST /akaun/token", s.uiAccountTokenAdd)
	mux.HandleFunc("POST /akaun/token/padam", s.uiAccountTokenRemove)
	mux.HandleFunc("POST /akaun/katalaluan", s.uiAccountPassword)
	mux.HandleFunc("GET /admin/audit", s.uiAudit)
	mux.HandleFunc("GET /admin/tetapan", s.uiSettings)
	mux.HandleFunc("GET /pemberitahuan", s.uiNotifications)
	mux.HandleFunc("POST /pemberitahuan/baca", s.uiNotificationsRead)
	mux.HandleFunc("GET /pemberitahuan/{id}", s.uiNotificationOpen)
	mux.HandleFunc("POST /akaun/emel", s.uiAccountEmail)
	mux.HandleFunc("GET /admin/emel", s.uiMailAdmin)
	mux.HandleFunc("POST /admin/emel", s.uiMailAdminSave)
	mux.HandleFunc("POST /admin/emel/uji", s.uiMailAdminTest)
	mux.HandleFunc("GET /org/baharu", s.uiOrgNew)
	mux.HandleFunc("POST /org/baharu", s.uiOrgNew)
	mux.HandleFunc("GET /org/{name}", s.uiOrg)
	mux.HandleFunc("POST /org/{name}/urus/{action}", s.uiOrgAction)
	mux.HandleFunc("GET /admin/data", s.uiData)
	mux.HandleFunc("GET /admin/data/audit.csv", s.uiDataAuditCSV)
	mux.HandleFunc("GET /admin/data/laporan.json", s.uiDataJSON)
	mux.HandleFunc("GET /akaun/data.json", s.uiMyData)
	mux.HandleFunc("GET /akaun/aktiviti", s.uiActivity)
	mux.HandleFunc("GET /login/oidc", s.uiOIDCStart)
	mux.HandleFunc("GET /login/oidc/balik", s.uiOIDCReturn)
	mux.HandleFunc("GET /admin/oidc", s.uiOIDCAdmin)
	mux.HandleFunc("POST /admin/oidc", s.uiOIDCAdminSave)
	mux.HandleFunc("POST /admin/oidc/uji", s.uiOIDCAdminTest)
	mux.HandleFunc("POST /akaun/oidc/padam", s.uiOIDCUnlink)
	mux.HandleFunc("GET /admin/ai", s.uiAIAdmin)
	mux.HandleFunc("POST /admin/ai", s.uiAIAdminSave)
	mux.HandleFunc("GET /admin/sandaran", s.uiCloud)
	mux.HandleFunc("POST /admin/sandaran", s.uiCloudSave)
	mux.HandleFunc("POST /admin/sandaran/kunci", s.uiCloudKey)
	mux.HandleFunc("POST /admin/sandaran/uji", s.uiCloudTest)
	mux.HandleFunc("POST /admin/sandaran/jalan", s.uiCloudRun)
	mux.HandleFunc("POST /admin/tetapan", s.uiSettingsSave)
	mux.HandleFunc("GET /{owner}", s.uiOwner)
	mux.HandleFunc("GET /{owner}/{repo}", s.uiRepo)
	mux.HandleFunc("GET /{owner}/{repo}/tree/{ref}", s.uiTree)
	mux.HandleFunc("GET /{owner}/{repo}/tree/{ref}/{path...}", s.uiTree)
	mux.HandleFunc("GET /{owner}/{repo}/blob/{ref}/{path...}", s.uiBlob)
	mux.HandleFunc("GET /{owner}/{repo}/raw/{ref}/{path...}", s.uiRaw)
	mux.HandleFunc("GET /{owner}/{repo}/blame/{ref}/{path...}", s.uiBlame)
	mux.HandleFunc("GET /{owner}/{repo}/commits/{ref}", s.uiLog)
	mux.HandleFunc("GET /{owner}/{repo}/commits/{ref}/{path...}", s.uiLog)
	mux.HandleFunc("GET /{owner}/{repo}/commit/{hash}", s.uiCommit)
	mux.HandleFunc("GET /{owner}/{repo}/refs", s.uiRefs)
	mux.HandleFunc("GET /{owner}/{repo}/cari", s.uiSearch)
	mux.HandleFunc("GET /{owner}/{repo}/isu", s.uiIssues)
	mux.HandleFunc("GET /{owner}/{repo}/isu/baharu", s.uiIssueNewForm)
	mux.HandleFunc("POST /{owner}/{repo}/isu", s.uiIssueCreate)
	mux.HandleFunc("GET /{owner}/{repo}/isu/{n}", s.uiIssue)
	mux.HandleFunc("POST /{owner}/{repo}/isu/{n}/komen", s.uiIssueComment)
	mux.HandleFunc("POST /{owner}/{repo}/isu/{n}/keadaan", s.uiIssueState)
	mux.HandleFunc("POST /{owner}/{repo}/isu/{n}/sunting", s.uiIssueEdit)
	mux.HandleFunc("POST /{owner}/{repo}/isu/{n}/label", s.uiIssueLabels)
	mux.HandleFunc("POST /{owner}/{repo}/isu/{n}/penerima", s.uiIssueAssign)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/penerima", s.uiPullAssign)
	mux.HandleFunc("GET /{owner}/{repo}/pr", s.uiPulls)
	mux.HandleFunc("GET /{owner}/{repo}/pr/baharu", s.uiPullNewForm)
	mux.HandleFunc("POST /{owner}/{repo}/pr", s.uiPullCreate)
	mux.HandleFunc("GET /{owner}/{repo}/pr/{n}", s.uiPull)
	mux.HandleFunc("GET /{owner}/{repo}/pr/{n}/fail", s.uiPullFiles)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/komen", s.uiPullComment)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/baris", s.uiPullLineComment)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/ai", s.uiPullAI)
	mux.HandleFunc("POST /{owner}/{repo}/import-github", s.uiImportGitHub)
	mux.HandleFunc("POST /{owner}/{repo}/fork", s.uiFork)
	mux.HandleFunc("GET /{owner}/{repo}/pr/hulu", s.uiUpstreamForm)
	mux.HandleFunc("POST /{owner}/{repo}/pr/hulu", s.uiUpstreamCreate)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/semak", s.uiPullReview)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/keadaan", s.uiPullState)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/sunting", s.uiPullEdit)
	mux.HandleFunc("POST /{owner}/{repo}/pr/{n}/gabung", s.uiPullMerge)
	mux.HandleFunc("POST /{owner}/{repo}/ikut", s.uiWatch)
	mux.HandleFunc("GET /{owner}/{repo}/tetapan", s.uiRepoSettings)
	mux.HandleFunc("POST /{owner}/{repo}/tetapan", s.uiRepoSettingsSave)
	mux.HandleFunc("POST /{owner}/{repo}/tetapan/lindung/{tindakan}", s.uiRepoProtect)
	mux.HandleFunc("GET /{owner}/{repo}/wiki", s.uiWikiHome)
	mux.HandleFunc("GET /{owner}/{repo}/wiki/_semua", s.uiWikiList)
	mux.HandleFunc("GET /{owner}/{repo}/wiki/_baharu", s.uiWikiEditForm)
	mux.HandleFunc("POST /{owner}/{repo}/wiki/_baharu", s.uiWikiSave)
	mux.HandleFunc("GET /{owner}/{repo}/wiki/{page}", s.uiWikiPage)
	mux.HandleFunc("GET /{owner}/{repo}/wiki/{page}/sunting", s.uiWikiEditForm)
	mux.HandleFunc("POST /{owner}/{repo}/wiki/{page}/sunting", s.uiWikiSave)
	mux.HandleFunc("GET /{owner}/{repo}/wiki/{page}/sejarah", s.uiWikiHistory)
	mux.HandleFunc("POST /{owner}/{repo}/wiki/{page}/padam", s.uiWikiDelete)
	mux.HandleFunc("GET /{owner}/{repo}/webhook", s.uiWebhooks)
	mux.HandleFunc("POST /{owner}/{repo}/webhook", s.uiWebhookCreate)
	mux.HandleFunc("GET /{owner}/{repo}/webhook/{id}", s.uiWebhook)
	mux.HandleFunc("POST /{owner}/{repo}/webhook/{id}/{action}", s.uiWebhookAction)
	mux.HandleFunc("GET /{owner}/{repo}/label", s.uiLabels)
	mux.HandleFunc("POST /{owner}/{repo}/label", s.uiLabelCreate)
	mux.HandleFunc("POST /{owner}/{repo}/label/padam", s.uiLabelDelete)
}

// staticHandler serves embedded assets under /static/<hash>/… with a year-long cache: the
// hash changes whenever CSS/JS change, so old pages never pick up new assets by accident.
func (s *Server) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, "static")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rest, ok := strings.CutPrefix(r.URL.Path, "/static/"+s.UI.staticHash+"/")
		if !ok || rest == "" || strings.Contains(rest, "..") {
			http.NotFound(w, r)
			return
		}
		// Revalidate, do not cache for a year: WebKit (Playwright 2336, Linux) serves a max-age
		// stylesheet from its memory cache on the NEXT navigation and then does not apply it —
		// second page unstyled, measured 14 Oct 2026 against five Cache-Control variants; only
		// max-age=0 + ETag (a 304 of ~100 bytes per page) renders every page. The URL already
		// carries the content hash, so a 304 is the whole cost. Re-test on a real iPhone before
		// going back to immutable.
		w.Header().Set("ETag", `"`+s.UI.staticHash+`"`)
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate, no-transform")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rest
		files.ServeHTTP(w, r2)
	})
}

// viewer resolves the session cookie to a user (nil when anonymous).
func (s *Server) viewer(r *http.Request) *store.User {
	c, err := r.Cookie(cookieSession)
	if err != nil || c.Value == "" {
		return nil
	}
	u, err := s.Store.SessionUser(r.Context(), c.Value)
	if err != nil || u.Disabled || u.IsOrg {
		return nil
	}
	return &u
}

// pageLang picks the page language: explicit cookie, then the user's, then Accept-Language, then site.
func (s *Server) pageLang(r *http.Request, u *store.User) string {
	if c, err := r.Cookie(cookieLang); err == nil {
		if l := i18n.Normalize(c.Value, ""); l != "" {
			return l
		}
	}
	return s.lang(r, u)
}

// csrf returns the double-submit token, setting the cookie on first sight.
func (s *Server) csrf(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(cookieCSRF); err == nil && len(c.Value) == 32 && isHexString(c.Value) {
		return c.Value
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	v := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{Name: cookieCSRF, Value: v, Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 86400 * 30})
	return v
}

// checkCSRF validates a POST: form field must equal the cookie.
func (s *Server) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(cookieCSRF)
	if err != nil || c.Value == "" {
		return false
	}
	return r.PostFormValue("csrf") == c.Value
}

func isHexString(v string) bool {
	_, err := hex.DecodeString(v)
	return err == nil
}

func (s *Server) newPage(w http.ResponseWriter, r *http.Request, title string) page {
	u := s.viewer(r)
	lang := s.pageLang(r, u)
	name := "Codr"
	if n, err := s.Store.Setting(r.Context(), "site.name"); err == nil && n != "" {
		name = n
	}
	other := "en-US"
	if lang == "en-US" {
		other = "ms-MY"
	}
	static := "/static/" + s.UI.staticHash
	unread := 0
	if u != nil {
		unread, _ = s.Store.UnreadCount(r.Context(), u.ID)
	}
	return page{Unread: unread, Lang: lang, User: u, CSRF: s.csrf(w, r), SiteName: name, Static: static,
		Version: version.String(), Path: r.URL.Path, Title: title, Now: time.Now(),
		OGImage: strings.TrimRight(s.UI.HTTPHost, "/") + static + "/og.png", OtherLang: other, Indexed: s.forgeIndexed(r.Context())}
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, code int, name string, p page) {
	if p.Title == "" {
		p.Title = p.SiteName
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; img-src 'self' https: data:; font-src 'self'; connect-src 'self'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'") // connect-src: the command palette's /api/cari (D68)
	w.WriteHeader(code)
	if err := s.UI.tmpl.ExecuteTemplate(w, name, p); err != nil {
		s.Log.Error("template", "name", name, "err", err)
	}
}

// errPage renders the localised error page for a status.
func (s *Server) errPage(w http.ResponseWriter, r *http.Request, code int) {
	p := s.newPage(w, r, "")
	key := "500"
	switch code {
	case http.StatusNotFound:
		key = "404"
	case http.StatusForbidden:
		key = "403"
	}
	p.Title = p.T("ui.error."+key+"_title") + " · " + p.SiteName
	p.Data = key
	s.render(w, r, code, "error.html", p)
}

// fail maps store/git errors to pages.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitread.ErrNotFound), errors.Is(err, gitread.ErrBadRef), errors.Is(err, gitread.ErrBadPath):
		s.errPage(w, r, http.StatusNotFound)
	default:
		s.Log.Error("ui", "path", r.URL.Path, "err", err)
		s.errPage(w, r, http.StatusInternalServerError)
	}
}

// setFlash stores a one-shot message in a cookie (survives a redirect, no server state).
func (s *Server) setFlash(w http.ResponseWriter, kind, msg string) {
	http.SetCookie(w, &http.Cookie{Name: "codr_flash", Value: kind + ":" + hex.EncodeToString([]byte(msg)), Path: "/", HttpOnly: true, Secure: s.UI.SecureCookies, SameSite: http.SameSiteLaxMode, MaxAge: 60})
}

func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request, p *page) {
	c, err := r.Cookie("codr_flash")
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "codr_flash", Value: "", Path: "/", MaxAge: -1})
	kind, encoded, ok := strings.Cut(c.Value, ":")
	if !ok {
		return
	}
	b, err := hex.DecodeString(encoded)
	if err != nil {
		return
	}
	p.Flash, p.FlashKind = string(b), kind
}

// hueClass picks one of eight monogram tile hues from a name, stably: the same repo always
// gets the same tile. Colours live in CSS (.h0–.h7); no inline style (D4).
func hueClass(name string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	return "h" + string(rune('0'+h%8))
}

// initialOf is the first letter of a name for the monogram tile.
func initialOf(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}
