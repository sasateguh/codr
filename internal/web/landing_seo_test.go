package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestLandingSEO: while the landing is closed there is no sitemap or llms.txt and no author links;
// once landing.index and the people settings are set, the page carries a schema.org graph naming them,
// robots.txt points at a sitemap with both languages, and llms.txt lists the pages and the source (D110).
func TestLandingSEO(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	h.s.LandingHosts = []string{"codr.my"}
	ctx := context.Background()

	for _, p := range []string{"/sitemap.xml", "/llms.txt"} {
		if resp, _ := landingGet(t, h, "GET", "codr.my", p); resp.StatusCode != 404 {
			t.Fatalf("%s while noindex: %d", p, resp.StatusCode)
		}
	}
	_, body := landingGet(t, h, "GET", "codr.my", "/")
	if strings.Contains(body, `rel="author"`) || strings.Contains(body, `"Person"`) || !strings.Contains(body, `application/ld+json`) {
		t.Fatalf("closed landing: people shown or graph missing: %.200s", body)
	}

	for k, v := range map[string]string{
		"landing.index": "true", "landing.source_url": "https://github.com/sasateguh/codr",
		"landing.author_name": "Aiman Malib", "landing.author_url": "https://aimanmalib.com",
		"landing.publisher_name": "Sasa Teguh", "landing.publisher_url": "https://sasateguh.com",
	} {
		if err := h.st.SetSetting(ctx, k, v, h.users["alice"].ID); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := landingGet(t, h, "GET", "codr.my", "/")
	if resp.StatusCode != 200 || resp.Header.Get("X-Robots-Tag") != "" || strings.Contains(body, `content="noindex`) {
		t.Fatalf("open landing still noindex: %v", resp.Header)
	}
	for _, want := range []string{`<meta property="og:image:width" content="1200">`, `<meta name="twitter:image"`, `<meta name="author" content="Aiman Malib">`,
		`href="https://aimanmalib.com" rel="author"`, `href="https://sasateguh.com"`, `href="https://github.com/sasateguh/codr" rel="noopener"`} {
		if !strings.Contains(body, want) {
			t.Errorf("open landing lacks %q", want)
		}
	}
	// the graph is valid JSON naming the app, its author and its publisher
	i := strings.Index(body, `<script type="application/ld+json">`)
	j := strings.Index(body[i:], "</script>")
	if i < 0 || j < 0 {
		t.Fatal("no JSON-LD block")
	}
	raw := body[i+len(`<script type="application/ld+json">`) : i+j]
	var g struct {
		Graph []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal([]byte(raw), &g); err != nil {
		t.Fatalf("JSON-LD is not JSON: %v\n%s", err, raw)
	}
	types := map[string]map[string]any{}
	for _, n := range g.Graph {
		types[n["@type"].(string)] = n
	}
	app := types["SoftwareApplication"]
	if app == nil || app["codeRepository"] != "https://github.com/sasateguh/codr" || types["Person"]["url"] != "https://aimanmalib.com" ||
		types["Organization"]["name"] != "Sasa Teguh" || app["author"] == nil || app["publisher"] == nil {
		t.Fatalf("graph: %s", raw)
	}

	if _, body := landingGet(t, h, "GET", "codr.my", "/robots.txt"); !strings.Contains(body, "Sitemap: http://codr.my/sitemap.xml") || strings.Contains(body, "Disallow") {
		t.Fatalf("robots open: %s", body)
	}
	resp, body = landingGet(t, h, "GET", "codr.my", "/sitemap.xml")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/xml") {
		t.Fatalf("sitemap: %d %v", resp.StatusCode, resp.Header)
	}
	for _, want := range []string{"<loc>http://codr.my/</loc>", "<loc>http://codr.my/ms</loc>", "<loc>http://codr.my/design</loc>", "<loc>http://codr.my/ms/design</loc>",
		`hreflang="ms" href="http://codr.my/ms"`} {
		if !strings.Contains(body, want) {
			t.Errorf("sitemap lacks %q", want)
		}
	}
	_, body = landingGet(t, h, "GET", "codr.my", "/llms.txt")
	for _, want := range []string{"# Codr", "AGPL-3.0", "(http://codr.my/ms)", "(https://github.com/sasateguh/codr)", "[Aiman Malib](https://aimanmalib.com)", "[Sasa Teguh](https://sasateguh.com)"} {
		if !strings.Contains(body, want) {
			t.Errorf("llms.txt lacks %q", want)
		}
	}

	// a link that is not http(s) is dropped, and the person with it
	if err := h.st.SetSetting(ctx, "landing.author_url", "javascript:alert(1)", h.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	if _, body := landingGet(t, h, "GET", "codr.my", "/"); strings.Contains(body, "javascript:") || strings.Contains(body, `rel="author"`) {
		t.Fatal("unsafe author link shown")
	}
}
