package web

import (
	"context"
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

// landingJSONLD is the schema.org graph for the landing page. html/template JSON-encodes it inside
// <script type="application/ld+json">, a data block that CSP does not execute.
func landingJSONLD(lang, name string, d landingData, ogImage, version string) map[string]any {
	base := strings.TrimSuffix(d.RootURL, "/")
	app := map[string]any{
		"@type":               "SoftwareApplication",
		"@id":                 base + "/#app",
		"name":                name,
		"url":                 d.Canonical,
		"description":         i18n.T(lang, "landing.description"),
		"applicationCategory": "DeveloperApplication",
		"operatingSystem":     "Linux",
		"softwareVersion":     strings.Fields(version + " ")[0],
		"license":             "https://www.gnu.org/licenses/agpl-3.0.html",
		"image":               ogImage,
		"inLanguage":          []string{"en", "ms"},
		"isAccessibleForFree": true,
		"offers":              map[string]any{"@type": "Offer", "price": "0", "priceCurrency": "USD"},
	}
	site := map[string]any{"@type": "WebSite", "@id": base + "/#website", "name": name, "url": d.RootURL,
		"inLanguage": []string{"en", "ms"}}
	graph := []any{app, site}
	if d.SourceURL != "" {
		app["codeRepository"] = d.SourceURL
		app["sameAs"] = []string{d.SourceURL}
	}
	if d.Author.URL != "" {
		id := strings.TrimSuffix(d.Author.URL, "/") + "/#person"
		graph = append(graph, map[string]any{"@type": "Person", "@id": id, "name": d.Author.Name, "url": d.Author.URL})
		app["author"] = map[string]any{"@id": id}
		app["creator"] = map[string]any{"@id": id}
	}
	if d.Publisher.URL != "" {
		id := strings.TrimSuffix(d.Publisher.URL, "/") + "/#org"
		graph = append(graph, map[string]any{"@type": "Organization", "@id": id, "name": d.Publisher.Name, "url": d.Publisher.URL})
		app["publisher"] = map[string]any{"@id": id}
		site["publisher"] = map[string]any{"@id": id}
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
	b.WriteString("# " + name + "\n\n> " + i18n.T(lang, "landing.description") + "\n\n")
	b.WriteString(i18n.T(lang, "landing.llms.licence") + "\n\n## " + i18n.T(lang, "landing.llms.pages") + "\n\n")
	b.WriteString("- [" + i18n.T("en-US", "landing.title") + "](" + base + landingPath("en-US", root) + ")\n")
	b.WriteString("- [" + i18n.T("ms-MY", "landing.title") + "](" + base + landingPath("ms-MY", root) + ")\n")
	b.WriteString("- [" + i18n.T("en-US", "landing.design.title") + "](" + base + designPath("en-US", root) + ")\n")
	b.WriteString("- [" + i18n.T("ms-MY", "landing.design.title") + "](" + base + designPath("ms-MY", root) + ")\n")
	if u := safeURL(strings.TrimSpace(src)); u != "" {
		b.WriteString("- [" + i18n.T(lang, "landing.install.source") + "](" + u + ")\n")
	}
	if author.URL != "" || publisher.URL != "" {
		b.WriteString("\n## " + i18n.T(lang, "landing.llms.people") + "\n\n")
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
