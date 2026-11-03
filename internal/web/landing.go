package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"codr.my/codr/internal/i18n"
	"codr.my/codr/internal/version"
)

// landingData is what landing.html needs beyond page. Every value comes from settings (D56).
type landingData struct {
	ForgeURL  string // where "open the forge" goes
	ForgeHost string // the same, shown as text
	DemoURL   string // a public example repository on the forge; "" = not shown
	SourceURL string // "" = not shown
	Contact   string // e-mail address; "" = not shown
	Index     bool
	Canonical string // absolute URL of this page
	Alternate string // absolute URL of the page in the other language
	MSURL     string
	ENURL     string
	RootURL   string // the page in the default language (x-default)
	Home      string // path of this page, for the brand link
	MSPath    string
	ENPath    string
	CloneURL  string // git clone address of the example repository; "" = not shown
	// Live figures measured on this server for the proof band (D60); 0 = not shown.
	Repos    int
	BinaryMB int
	MemoryMB int
	// Commits labels the 3D graph with the example repository's real history (JSON, newest first).
	Commits string
	// Next is the "What comes next" list from landing.roadmap (D69).
	Next []nextItem
	// NextDone is set when every roadmap item is live: the section then reads as a record, not a plan.
	NextDone bool
	// Author and Publisher come from landing.author_* / landing.publisher_* (D110); empty = not shown.
	Author    landingPerson
	Publisher landingPerson
	// JSONLD is the schema.org graph, JSON-encoded by html/template in a data block (D110).
	JSONLD map[string]any
	// DesignPath is the design write-up in this language (D90).
	DesignPath string
	// Rejected is the real message a force push to a protected branch gets, for the terminal section.
	Rejected string
}

// nextItem is one row of the landing roadmap: a locale key stem and its status.
type nextItem struct {
	Key    string // landing.next.<Key>.t / .d
	Status string // live | soon | planned
}

// parseRoadmap reads landing.roadmap ("key=status,…"); unknown statuses and keys without text are dropped.
func parseRoadmap(v, lang string) []nextItem {
	var out []nextItem
	for _, part := range strings.Split(v, ",") {
		k, st, ok := strings.Cut(strings.TrimSpace(part), "=")
		k, st = strings.TrimSpace(k), strings.TrimSpace(st)
		if !ok || (st != "live" && st != "soon" && st != "planned") || k == "" {
			continue
		}
		if i18n.T(lang, "landing.next."+k+".t") == "landing.next."+k+".t" { // no text for this key: not shown
			continue
		}
		out = append(out, nextItem{Key: k, Status: st})
	}
	return out
}

// landingHost reports whether a request's Host is one of site.landing_hosts.
func (s *Server) landingHost(r *http.Request) (string, bool) {
	if len(s.LandingHosts) == 0 {
		return "", false
	}
	h := requestHost(r)
	for _, l := range s.LandingHosts {
		if h == l {
			return h, true
		}
	}
	return "", false
}

// requestHost is the Host header without port or trailing dot, lower case.
func requestHost(r *http.Request) string {
	h := r.Host
	if hp, _, err := net.SplitHostPort(h); err == nil {
		h = hp
	}
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// scheme is the public scheme, taken from http_host (the TLS ends at the tunnel, not here).
func (s *Server) scheme() string {
	if strings.HasPrefix(s.UI.HTTPHost, "https://") {
		return "https"
	}
	return "http"
}

// landing serves a landing host: the page in two languages, robots.txt, static files and
// healthz. Other GETs go to the forge; the git doors are not on this name (D56).
func (s *Server) landing(w http.ResponseWriter, r *http.Request, host string) {
	canon := s.LandingHosts[0]
	if host != canon {
		http.Redirect(w, r, s.scheme()+"://"+canon+r.URL.RequestURI(), http.StatusMovedPermanently)
		return
	}
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/static/"):
		s.staticHandler().ServeHTTP(w, r)
	case p == "/healthz":
		s.healthz(w, r)
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		s.text(w, r, http.StatusNotFound, "http.no_page")
	case p == "/":
		s.landingPage(w, r, s.landingRoot(r.Context()))
	case p == "/en" || p == "/ms" || p == "/en/" || p == "/ms/":
		// the default language lives at "/", the other one at its short code (D60)
		lang := map[string]string{"en": "en-US", "ms": "ms-MY"}[strings.Trim(p, "/")]
		if lang == s.landingRoot(r.Context()) {
			http.Redirect(w, r, "/", http.StatusMovedPermanently)
		} else if strings.HasSuffix(p, "/") {
			http.Redirect(w, r, strings.TrimSuffix(p, "/"), http.StatusMovedPermanently)
		} else {
			s.landingPage(w, r, lang)
		}
	case p == "/design" || p == "/en/design" || p == "/ms/design":
		lang := s.landingRoot(r.Context())
		if p != "/design" {
			lang = map[string]string{"en": "en-US", "ms": "ms-MY"}[p[1:3]]
			if lang == s.landingRoot(r.Context()) {
				http.Redirect(w, r, "/design", http.StatusMovedPermanently)
				return
			}
		}
		s.designPage(w, r, lang)
	case p == "/robots.txt":
		s.landingRobots(w, r)
	case p == "/sitemap.xml":
		s.landingSitemap(w, r)
	case p == "/llms.txt":
		s.landingLLMs(w, r)
	default:
		http.Redirect(w, r, s.forgeURL(r.Context())+r.URL.RequestURI(), http.StatusFound)
	}
}

// forgeURL is landing.forge_url, else http_host, without a trailing slash.
func (s *Server) forgeURL(ctx context.Context) string {
	if v, err := s.Store.Setting(ctx, "landing.forge_url"); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimRight(strings.TrimSpace(v), "/")
	}
	return strings.TrimRight(s.UI.HTTPHost, "/")
}

// landingRoot is the language served at "/": the site default (setting site.default_language).
func (s *Server) landingRoot(ctx context.Context) string {
	if v, err := s.Store.Setting(ctx, "site.default_language"); err == nil {
		if l := i18n.Normalize(v, ""); l == "ms-MY" || l == "en-US" {
			return l
		}
	}
	if s.DefaultLang == "ms-MY" {
		return "ms-MY"
	}
	return "en-US"
}

// landingPath is where a language's page lives given the root language.
func landingPath(lang, root string) string {
	if lang == root {
		return "/"
	}
	return "/" + lang[:2]
}

func (s *Server) landingIndex(ctx context.Context) bool {
	v, err := s.Store.SettingBool(ctx, "landing.index")
	return err == nil && v
}

func (s *Server) landingRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if s.landingIndex(r.Context()) {
		_, _ = w.Write([]byte("User-agent: *\nAllow: /\n\nSitemap: " + s.scheme() + "://" + s.LandingHosts[0] + "/sitemap.xml\n"))
		return
	}
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
}

func (s *Server) landingPage(w http.ResponseWriter, r *http.Request, lang string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	setting := func(k string) string { v, _ := s.Store.Setting(ctx, k); return strings.TrimSpace(v) }
	base := s.scheme() + "://" + s.LandingHosts[0]
	forge := s.forgeURL(ctx)
	root := s.landingRoot(ctx)
	d := landingData{ForgeURL: forge, SourceURL: safeURL(setting("landing.source_url")), Index: s.landingIndex(ctx),
		MSPath: landingPath("ms-MY", root), ENPath: landingPath("en-US", root), Home: landingPath(lang, root)}
	d.MSURL, d.ENURL, d.RootURL = base+d.MSPath, base+d.ENPath, base+"/"
	d.DesignPath = designPath(lang, root)
	d.Repos, d.BinaryMB, d.MemoryMB = s.landingFigures(ctx)
	if u, err := url.Parse(forge); err == nil {
		d.ForgeHost = u.Host
	}
	if demo := setting("landing.demo_repo"); demo != "" && !strings.ContainsAny(demo, " ?#") && strings.Count(demo, "/") == 1 {
		d.DemoURL = forge + "/" + demo
		d.CloneURL = forge + "/" + demo + ".git"
		d.Commits = s.landingCommits(ctx, demo)
	}
	d.Rejected = i18n.T(lang, "hook.protected_force", "main", "aiman/contoh")
	d.Next = parseRoadmap(setting("landing.roadmap"), lang)
	d.NextDone = len(d.Next) > 0
	for _, n := range d.Next {
		if n.Status != "live" {
			d.NextDone = false
		}
	}
	if c := setting("landing.contact_email"); strings.Count(c, "@") == 1 && !strings.ContainsAny(c, " <>\"?") {
		d.Contact = c
	}
	d.Canonical, d.Alternate = d.MSURL, d.ENURL
	other := "en-US"
	if lang == "en-US" {
		d.Canonical, d.Alternate, other = d.ENURL, d.MSURL, "ms-MY"
	}
	name := "Codr"
	if n, err := s.Store.Setting(ctx, "site.name"); err == nil && n != "" {
		name = n
	}
	static := "/static/" + s.UI.staticHash
	d.Author, d.Publisher = s.landingPeople(ctx)
	og := base + static + "/og.png"
	d.JSONLD = landingJSONLD(lang, name, d, og, version.String())
	p := page{Lang: lang, CSRF: "", SiteName: name, Static: static, Version: version.String(), Path: r.URL.Path,
		Title: i18n.T(lang, "landing.title"), Now: time.Now(), OGImage: og, OtherLang: other, Data: d}
	if !d.Index {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	s.render(w, r, http.StatusOK, "landing.html", p)
}

// landingFigures are the live numbers of the proof band: repositories on this install, the size of the
// running binary and the memory the Go runtime holds from the OS. A figure that cannot be read is 0 (hidden).
func (s *Server) landingFigures(ctx context.Context) (repos, binaryMB, memoryMB int) {
	repos, _ = s.Store.RepoCount(ctx)
	if exe, err := os.Executable(); err == nil {
		if fi, err := os.Stat(exe); err == nil {
			binaryMB = int((fi.Size() + 1<<19) >> 20)
		}
	}
	memoryMB = int((residentBytes() + 1<<19) >> 20)
	return repos, binaryMB, memoryMB
}

// residentBytes is the memory the process really occupies: VmRSS from /proc/self/status on Linux. Elsewhere
// (or unreadable) it is 0 and the figure is hidden; runtime.MemStats.Sys is address space reserved from the
// OS, not memory in use, and overstated the figure by a quarter on the live server (16 Oct 2026, D66).
func residentBytes() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) == 2 && f[1] == "kB" {
				kb, err := strconv.ParseUint(f[0], 10, 64)
				if err == nil {
					return kb << 10
				}
			}
		}
	}
	return 0
}

// landingCommits returns up to eight commits of a PUBLIC repository as JSON [{"h":short,"s":subject}];
// anything else (private, missing, empty) is "" and the graph simply has no labels.
func (s *Server) landingCommits(ctx context.Context, ref string) string {
	owner, name, _ := strings.Cut(ref, "/")
	repo, err := s.Store.RepoByPath(ctx, owner, name)
	if err != nil || repo.Private {
		return ""
	}
	cs, err := s.git(repo.ID).Log(ctx, repo.DefaultBranch, "", 8, 0)
	if err != nil || len(cs) == 0 {
		return ""
	}
	type label struct {
		H string `json:"h"`
		S string `json:"s"`
	}
	out := make([]label, 0, len(cs))
	for _, c := range cs {
		subj := c.Subject
		if r := []rune(subj); len(r) > 44 {
			subj = string(r[:43]) + "…"
		}
		out = append(out, label{H: c.Short(), S: subj})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// safeURL keeps an operator-written link only when it is http(s); anything else is dropped.
func safeURL(v string) string {
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return v
}
