package web

import (
	"context"
	"html/template"
	"net/http"
	"strings"

	"codr.my/codr/internal/i18n"
)

// Search and sharing metadata for the landing host (D110): a sitemap with both languages, llms.txt, and
// the schema.org description of the product, its author and its publisher. Every name and link comes from
// the settings table (landing.author_*, landing.publisher_*, landing.source_url); nothing is typed here.
// Sitemap and llms.txt exist only while landing.index is on, so a closed site offers no list to crawl.

// landingPeople are the author and publisher of the product, from settings; an entry without a safe
// https link is dropped.
type landingPerson struct {
	Name string
	URL  string
}

func (s *Server) landingPeople(ctx context.Context) (author, publisher landingPerson) {
	setting := func(k string) string { v, _ := s.Store.Setting(ctx, k); return strings.TrimSpace(v) }
	author = landingPerson{Name: setting("landing.author_name"), URL: safeURL(setting("landing.author_url"))}
	publisher = landingPerson{Name: setting("landing.publisher_name"), URL: safeURL(setting("landing.publisher_url"))}
	if author.Name == "" || author.URL == "" {
		author = landingPerson{}
	}
	if publisher.Name == "" || publisher.URL == "" {
		publisher = landingPerson{}
	}
	return author, publisher
}

// modulePath is the Go import path of this module (go.mod); `go install codr.my/codr/cmd/codr@latest` asks the
// landing host for it with ?go-get=1 and is pointed at landing.source_url.
const modulePath = "/codr"

// goImport answers the go command's ?go-get=1 probe with the go-import meta tag (vanity import path).
func (s *Server) goImport(w http.ResponseWriter, r *http.Request) {
	src, _ := s.Store.Setting(r.Context(), "landing.source_url")
	src = safeURL(strings.TrimSpace(src))
	if src == "" {
		s.text(w, r, http.StatusNotFound, "http.no_page")
		return
	}
	mod := template.HTMLEscapeString(s.LandingHosts[0] + modulePath)
	esc := template.HTMLEscapeString(src)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><meta name="go-import" content="` + mod + ` git ` + esc + `">` +
		`<meta name="go-source" content="` + mod + ` ` + esc + ` ` + esc + `/tree/main{/dir} ` + esc + `/blob/main{/dir}/{file}#L{line}">`))
}

// faqItem is one visible question on the landing page; the same text feeds the FAQPage graph.
type faqItem struct{ Q, A string }

// landingFAQ is the question list in lang. The last question names the author and publisher and is shown only
// when both are set (D110).
func landingFAQ(lang string, d landingData) []faqItem {
	var out []faqItem
	for _, k := range []string{"gitea", "postgres", "licence", "install", "github"} {
		out = append(out, faqItem{Q: i18n.T(lang, "landing.faq."+k+".q"), A: i18n.T(lang, "landing.faq."+k+".a")})
	}
	if d.Author.Name != "" && d.Publisher.Name != "" {
		out = append(out, faqItem{Q: i18n.T(lang, "landing.faq.who.q"), A: i18n.T(lang, "landing.faq.who.a", d.Author.Name, d.Publisher.Name)})
	}
	return out
}

// peopleNodes adds the author and publisher to a graph and returns their references ("" when not set).
func peopleNodes(d landingData, graph *[]any) (author, publisher map[string]any) {
	if d.Author.URL != "" {
		id := strings.TrimSuffix(d.Author.URL, "/") + "/#person"
		*graph = append(*graph, map[string]any{"@type": "Person", "@id": id, "name": d.Author.Name, "url": d.Author.URL})
		author = map[string]any{"@id": id}
	}
	if d.Publisher.URL != "" {
		id := strings.TrimSuffix(d.Publisher.URL, "/") + "/#org"
		*graph = append(*graph, map[string]any{"@type": "Organization", "@id": id, "name": d.Publisher.Name, "url": d.Publisher.URL})
		publisher = map[string]any{"@id": id}
	}
	return author, publisher
}

// landingJSONLD is the schema.org graph for the landing page. html/template JSON-encodes it inside
// <script type="application/ld+json">, a data block that CSP does not execute. The app node is the same on both
// language pages (url = the root); what differs per language lives on the WebPage node.
func landingJSONLD(lang, name string, d landingData, ogImage, shot, version string) map[string]any {
	base := strings.TrimSuffix(d.RootURL, "/")
	app := map[string]any{
		"@type":                  "SoftwareApplication",
		"@id":                    base + "/#app",
		"name":                   name,
		"alternateName":          name + " git forge",
		"url":                    d.RootURL,
		"description":            i18n.T("en-US", "landing.description"),
		"applicationCategory":    "DeveloperApplication",
		"applicationSubCategory": "Git forge",
		"operatingSystem":        "Linux",
		"softwareVersion":        strings.TrimPrefix(strings.Fields(version + " ")[0], "v"),
		"softwareRequirements":   i18n.T("en-US", "landing.requirements"),
		"programmingLanguage":    "Go",
		"license":                "https://www.gnu.org/licenses/agpl-3.0.html",
		"image":                  ogImage,
		"screenshot":             shot,
		"inLanguage":             []string{"en", "ms"},
		"isAccessibleForFree":    true,
		"offers":                 map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
	}
	site := map[string]any{"@type": "WebSite", "@id": base + "/#website", "name": name, "url": d.RootURL, "inLanguage": []string{"en", "ms"}}
	pageNode := map[string]any{"@type": "WebPage", "@id": d.Canonical + "#page", "url": d.Canonical, "name": i18n.T(lang, "landing.title"),
		"description": i18n.T(lang, "landing.description"), "inLanguage": lang[:2], "isPartOf": map[string]any{"@id": base + "/#website"},
		"about": map[string]any{"@id": base + "/#app"}}
	graph := []any{app, site, pageNode}
	if d.SourceURL != "" {
		app["codeRepository"] = d.SourceURL
		app["sameAs"] = []string{d.SourceURL}
		if strings.HasPrefix(d.SourceURL, "https://github.com/") {
			app["downloadUrl"] = strings.TrimSuffix(d.SourceURL, "/") + "/releases/latest"
		}
	}
	if a, p := peopleNodes(d, &graph); a != nil || p != nil {
		if a != nil {
			app["author"], app["creator"] = a, a
		}
		if p != nil {
			app["publisher"], site["publisher"] = p, p
		}
	}
	if len(d.FAQ) > 0 {
		var qs []any
		for _, f := range d.FAQ {
			qs = append(qs, map[string]any{"@type": "Question", "name": f.Q, "acceptedAnswer": map[string]any{"@type": "Answer", "text": f.A}})
		}
		graph = append(graph, map[string]any{"@type": "FAQPage", "@id": d.Canonical + "#faq", "inLanguage": lang[:2], "mainEntity": qs})
	}
	return map[string]any{"@context": "https://schema.org", "@graph": graph}
}

// designJSONLD describes the design write-up as a TechArticle about the app, by the same people.
func designJSONLD(lang, title, base string, d landingData, ogImage string) map[string]any {
	art := map[string]any{"@type": "TechArticle", "@id": d.Canonical + "#article", "url": d.Canonical, "headline": title,
		"description": i18n.T(lang, "landing.design.description"), "inLanguage": lang[:2], "image": ogImage,
		"about": map[string]any{"@id": base + "/#app"}, "isPartOf": map[string]any{"@id": base + "/#website"}}
	graph := []any{art}
	if a, p := peopleNodes(d, &graph); a != nil || p != nil {
		if a != nil {
			art["author"] = a
		}
		if p != nil {
			art["publisher"] = p
		}
	}
	return map[string]any{"@context": "https://schema.org", "@graph": graph}
}

// landingSitemap lists the four landing pages, each with its other-language alternate (hreflang).
func (s *Server) landingSitemap(w http.ResponseWriter, r *http.Request) {
	if !s.landingIndex(r.Context()) {
		s.text(w, r, http.StatusNotFound, "http.no_page")
		return
	}
	base := s.scheme() + "://" + s.LandingHosts[0]
	root := s.landingRoot(r.Context())
	pairs := [][2]string{
		{landingPath("en-US", root), landingPath("ms-MY", root)},
		{designPath("en-US", root), designPath("ms-MY", root)},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n")
	for _, p := range pairs {
		for _, loc := range p {
			b.WriteString("<url><loc>" + base + loc + "</loc>")
			b.WriteString(`<xhtml:link rel="alternate" hreflang="en" href="` + base + p[0] + `"/>`)
			b.WriteString(`<xhtml:link rel="alternate" hreflang="ms" href="` + base + p[1] + `"/>`)
			b.WriteString(`<xhtml:link rel="alternate" hreflang="x-default" href="` + base + p[0] + `"/>`)
			b.WriteString("</url>\n")
		}
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// landingLLMs is llms.txt: what the product is and where its pages, source and people are, as plain text.
func (s *Server) landingLLMs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.landingIndex(ctx) {
		s.text(w, r, http.StatusNotFound, "http.no_page")
		return
	}
	base := s.scheme() + "://" + s.LandingHosts[0]
	root := s.landingRoot(ctx)
	name := "Codr"
	if n, err := s.Store.Setting(ctx, "site.name"); err == nil && n != "" {
		name = n
	}
	src, _ := s.Store.Setting(ctx, "landing.source_url")
	author, publisher := s.landingPeople(ctx)
	lang := "en-US"
	var b strings.Builder
	line := func(title, url, note string) { b.WriteString("- [" + title + "](" + url + "): " + note + "\n") }
	b.WriteString("# " + name + "\n\n> " + i18n.T(lang, "landing.description") + "\n\n")
	b.WriteString(i18n.T(lang, "landing.llms.licence") + " " + i18n.T(lang, "landing.llms.needs", i18n.T(lang, "landing.requirements")) + "\n\n")
	b.WriteString("## " + i18n.T(lang, "landing.llms.pages") + "\n\n")
	line(i18n.T("en-US", "landing.title"), base+landingPath("en-US", root), i18n.T(lang, "landing.llms.note_home"))
	line(i18n.T("ms-MY", "landing.title"), base+landingPath("ms-MY", root), i18n.T(lang, "landing.llms.note_home_ms"))
	line(i18n.T("en-US", "landing.design.title"), base+designPath("en-US", root), i18n.T(lang, "landing.llms.note_design"))
	line(i18n.T("ms-MY", "landing.design.title"), base+designPath("ms-MY", root), i18n.T(lang, "landing.llms.note_design_ms"))
	if u := safeURL(strings.TrimSpace(src)); u != "" {
		b.WriteString("\n## " + i18n.T(lang, "landing.llms.code") + "\n\n")
		line(i18n.T(lang, "landing.install.source"), u, i18n.T(lang, "landing.llms.note_source"))
		if strings.HasPrefix(u, "https://github.com/") {
			u = strings.TrimSuffix(u, "/")
			line(i18n.T(lang, "landing.llms.releases"), u+"/releases/latest", i18n.T(lang, "landing.llms.note_releases"))
			raw := "https://raw.githubusercontent.com/" + strings.TrimPrefix(u, "https://github.com/") + "/main/"
			line("README.md", raw+"README.md", i18n.T(lang, "landing.llms.note_readme"))
			line("docs/ci.md", raw+"docs/ci.md", i18n.T(lang, "landing.llms.note_ci"))
		}
	}
	b.WriteString("\n## " + i18n.T(lang, "landing.faq.title") + "\n\n")
	d := landingData{Author: author, Publisher: publisher}
	for _, f := range landingFAQ(lang, d) {
		b.WriteString("### " + f.Q + "\n\n" + f.A + "\n\n")
	}
	if author.URL != "" || publisher.URL != "" {
		b.WriteString("## " + i18n.T(lang, "landing.llms.people") + "\n\n")
		if author.URL != "" {
			b.WriteString("- " + i18n.T(lang, "landing.llms.author") + ": [" + author.Name + "](" + author.URL + ")\n")
		}
		if publisher.URL != "" {
			b.WriteString("- " + i18n.T(lang, "landing.llms.publisher") + ": [" + publisher.Name + "](" + publisher.URL + ")\n")
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
