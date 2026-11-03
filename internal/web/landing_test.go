package web

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
)

// landingGet requests path on the test server with the given Host header, without following redirects.
func landingGet(t *testing.T, h *harness, method, host, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, h.srv.URL+path, nil)
	req.Host = host
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// TestLanding: the landing host gets the bilingual product page and nothing of the forge (D56);
// every operator value comes from settings; noindex is the default on all three layers.
func TestLanding(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	h.s.LandingHosts = []string{"amcd.my", "www.amcd.my"}
	ctx := context.Background()

	// English is the default language (D60): "/" is English, Malay lives at /ms
	resp, body := landingGet(t, h, "GET", "amcd.my", "/")
	if resp.StatusCode != 200 || !strings.Contains(body, `lang="en-US"`) || !strings.Contains(body, "Your code, on your own machine.") {
		t.Fatalf("landing en: %d %.300s", resp.StatusCode, body)
	}
	for _, want := range []string{`<meta name="robots" content="noindex, nofollow">`, `hreflang="ms" href="http://amcd.my/ms"`, `hreflang="x-default" href="http://amcd.my/"`,
		`rel="canonical" href="http://amcd.my/"`, "/pendaratan.js", `id="graf3d"`, `href="http://localhost:3000"`, `class="p-nilai"`} {
		if !strings.Contains(body, want) {
			t.Errorf("landing lacks %q", want)
		}
	}
	if resp.Header.Get("X-Robots-Tag") == "" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'self'") || !strings.Contains(resp.Header.Get("Cache-Control"), "no-transform") {
		t.Fatalf("landing headers: %v", resp.Header)
	}
	for _, hidden := range []string{"mailto:", "See an example repository", "Source code", `class="p-klon"`, "/alice/awam", "csrf"} {
		if strings.Contains(body, hidden) {
			t.Errorf("landing shows %q while its setting is empty / it is the forge's", hidden)
		}
	}
	if _, body := landingGet(t, h, "GET", "amcd.my", "/ms"); !strings.Contains(body, `lang="ms-MY"`) || !strings.Contains(body, "Kod anda, di mesin anda.") || !strings.Contains(body, `rel="canonical" href="http://amcd.my/ms"`) {
		t.Fatalf("landing ms: %.300s", body)
	}
	// the default language has one address; old /en links and trailing slashes land on it
	for path, to := range map[string]string{"/en": "/", "/en/": "/", "/ms/": "/ms"} {
		if resp, _ := landingGet(t, h, "GET", "amcd.my", path); resp.StatusCode != 301 || resp.Header.Get("Location") != to {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp, body := landingGet(t, h, "GET", "amcd.my", "/robots.txt"); resp.StatusCode != 200 || !strings.Contains(body, "Disallow: /") {
		t.Fatalf("robots: %d %s", resp.StatusCode, body)
	}
	// the other names redirect to the canonical one; the forge is not on the landing host
	if resp, _ := landingGet(t, h, "GET", "www.amcd.my:443", "/ms?x=1"); resp.StatusCode != 301 || resp.Header.Get("Location") != "http://amcd.my/ms?x=1" {
		t.Fatalf("www: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := landingGet(t, h, "GET", "amcd.my", "/alice/awam"); resp.StatusCode != 302 || resp.Header.Get("Location") != "http://localhost:3000/alice/awam" {
		t.Fatalf("forge path: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := landingGet(t, h, "GET", "amcd.my", "/alice/awam.git/info/refs?service=git-upload-pack"); resp.StatusCode != 302 {
		t.Fatalf("git door must not answer on the landing host: %d", resp.StatusCode)
	}
	if resp, _ := landingGet(t, h, "POST", "amcd.my", "/login"); resp.StatusCode != 404 {
		t.Fatalf("POST on landing: %d", resp.StatusCode)
	}
	if resp, _ := landingGet(t, h, "GET", "amcd.my", "/healthz"); resp.StatusCode != 200 {
		t.Fatalf("healthz on landing: %d", resp.StatusCode)
	}
	if resp, _ := landingGet(t, h, "GET", "amcd.my", "/static/"+h.s.UI.staticHash+"/pendaratan.css"); resp.StatusCode != 200 {
		t.Fatalf("static on landing: %d", resp.StatusCode)
	}
	// an old name moves to the new one: GET 301, anything else 308 (a git push keeps its method)
	h.s.RedirectHosts = map[string]string{"forge.amcd.my": "git.codr.my"}
	if resp, _ := landingGet(t, h, "GET", "forge.amcd.my", "/alice/awam?x=1"); resp.StatusCode != 301 || resp.Header.Get("Location") != "http://git.codr.my/alice/awam?x=1" {
		t.Fatalf("redirect host GET: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := landingGet(t, h, "POST", "forge.amcd.my", "/alice/awam.git/git-receive-pack"); resp.StatusCode != 308 {
		t.Fatalf("redirect host POST: %d", resp.StatusCode)
	}
	// the forge's own host is untouched
	if _, body := landingGet(t, h, "GET", "127.0.0.1", "/"); !strings.Contains(body, `href="/alice/awam"`) {
		t.Fatal("forge home changed by the landing hosts")
	}

	// settings drive the links; unsafe values are dropped
	for k, v := range map[string]string{"landing.index": "true", "landing.demo_repo": "alice/awam", "landing.contact_email": "hai@amcd.my",
		"landing.source_url": "javascript:alert(1)", "landing.forge_url": "https://forge.example.my/"} {
		if err := h.st.SetSetting(ctx, k, v, h.users["alice"].ID); err != nil {
			t.Fatal(k, err)
		}
	}
	resp, body = landingGet(t, h, "GET", "amcd.my", "/")
	if strings.Contains(body, "noindex") || resp.Header.Get("X-Robots-Tag") != "" {
		t.Fatal("landing.index=true must drop noindex")
	}
	for _, want := range []string{`href="https://forge.example.my/alice/awam"`, `href="mailto:hai@amcd.my"`, "running at forge.example.my today", "git clone https://forge.example.my/alice/awam.git"} {
		if !strings.Contains(body, want) {
			t.Errorf("landing with settings lacks %q", want)
		}
	}
	if strings.Contains(body, "javascript:") {
		t.Fatal("unsafe source_url reached the page")
	}
	if _, body := landingGet(t, h, "GET", "amcd.my", "/robots.txt"); !strings.Contains(body, "Allow: /") {
		t.Fatal("robots must allow when indexed")
	}

	// an operator who wants Malay at "/" sets site.default_language; English moves to /en
	if err := h.st.SetSetting(ctx, "site.default_language", "ms-MY", h.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	if _, body := landingGet(t, h, "GET", "amcd.my", "/"); !strings.Contains(body, `lang="ms-MY"`) || !strings.Contains(body, `hreflang="en" href="http://amcd.my/en"`) {
		t.Fatalf("root in Malay: %.300s", body)
	}
	if resp, _ := landingGet(t, h, "GET", "amcd.my", "/ms"); resp.StatusCode != 301 || resp.Header.Get("Location") != "/" {
		t.Fatalf("/ms when Malay is the root: %d", resp.StatusCode)
	}
}

// TestForgeIndexSwitch: the forge is noindex on all three layers until site.index is true (D66).
func TestForgeIndexSwitch(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	resp, body := landingGet(t, h, "GET", "127.0.0.1", "/robots.txt")
	if resp.StatusCode != 200 || !strings.Contains(body, "Disallow: /") || resp.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("robots.txt: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	for _, p := range []string{"/", "/alice/awam", "/alice/awam/raw/main/README.md"} {
		resp, body := landingGet(t, h, "GET", "127.0.0.1", p)
		if resp.Header.Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Errorf("%s lacks X-Robots-Tag", p)
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && !strings.Contains(body, `<meta name="robots" content="noindex, nofollow">`) {
			t.Errorf("%s lacks the robots meta", p)
		}
	}
	if err := h.st.SetSetting(context.Background(), "site.index", "true", h.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	resp, body = landingGet(t, h, "GET", "127.0.0.1", "/robots.txt")
	if !strings.Contains(body, "Allow: /") || resp.Header.Get("X-Robots-Tag") != "" {
		t.Fatalf("indexed robots: %q %v", body, resp.Header)
	}
	if _, body := landingGet(t, h, "GET", "127.0.0.1", "/alice/awam"); strings.Contains(body, `name="robots"`) {
		t.Fatal("meta noindex kept with site.index=true")
	}
}

// TestResidentBytes: the memory figure is the process's real resident size, not reserved address space (D66).
func TestResidentBytes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("VmRSS is Linux only; elsewhere the figure is hidden")
	}
	if n := residentBytes(); n < 1<<20 || n > 1<<34 {
		t.Fatalf("resident %d bytes is not plausible", n)
	}
}

// TestLandingRoadmap: the "What comes next" list follows landing.roadmap; bad entries are dropped (D69).
func TestLandingRoadmap(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	h.s.LandingHosts = []string{"amcd.my"}
	_, body := landingGet(t, h, "GET", "amcd.my", "/")
	if !strings.Contains(body, "What comes next") || !strings.Contains(body, "Backups to your own cloud") || strings.Contains(body, "p-status-live") {
		t.Fatalf("default roadmap: %s", between(body, `id="seterusnya"`, 400))
	}
	if err := h.st.SetSetting(context.Background(), "landing.roadmap", "cloud=live, nope=soon, ai=weird, orgs=planned", h.users["alice"].ID); err != nil {
		t.Fatal(err)
	}
	_, body = landingGet(t, h, "GET", "amcd.my", "/")
	if strings.Count(body, "<li class=\"p-muncul p-status-") != 2 || !strings.Contains(body, `p-status-live"><span class="p-lencana-status">Live`) || strings.Contains(body, "An AI assistant") {
		t.Fatalf("edited roadmap: %s", between(body, `id="seterusnya"`, 900))
	}
	if !strings.Contains(body, "What comes next") || strings.Contains(body, "Built since launch") {
		t.Fatal("a roadmap with planned items must keep the plan heading")
	}
	_ = h.st.SetSetting(context.Background(), "landing.roadmap", "cloud=live,orgs=live", h.users["alice"].ID)
	if _, body := landingGet(t, h, "GET", "amcd.my", "/"); !strings.Contains(body, "Built since launch") || strings.Contains(body, "What comes next") {
		t.Fatalf("an all-live roadmap reads as a record: %s", between(body, `id="seterusnya"`, 400))
	}
	_ = h.st.SetSetting(context.Background(), "landing.roadmap", "", h.users["alice"].ID)
	if _, body := landingGet(t, h, "GET", "amcd.my", "/"); strings.Contains(body, `id="seterusnya"`) {
		t.Fatal("empty roadmap must hide the section")
	}
}

// TestDesignPage (D90): the design write-up on the landing host in both languages, from docs/*.md, noindex while the
// site is, linked from the landing page, never on the forge host.
func TestDesignPage(t *testing.T) {
	h := newHarness(t)
	h.s.LandingHosts = []string{"codr.my"}
	resp, body := landingGet(t, h, "GET", "codr.my", "/design")
	if resp.StatusCode != 200 || !strings.Contains(body, `lang="en-US"`) || !strings.Contains(body, "<h1") || !strings.Contains(body, "How Codr is built") ||
		!strings.Contains(body, "AccessFor") || resp.Header.Get("X-Robots-Tag") == "" || !strings.Contains(body, `hreflang="ms" href="http://codr.my/ms/design"`) {
		t.Fatalf("design en: %d %.400s", resp.StatusCode, body)
	}
	resp, body = landingGet(t, h, "GET", "codr.my", "/ms/design")
	if resp.StatusCode != 200 || !strings.Contains(body, `lang="ms-MY"`) || !strings.Contains(body, "Bagaimana Codr dibina") || strings.Contains(body, "How Codr is built") {
		t.Fatalf("design ms: %d", resp.StatusCode)
	}
	if resp, _ := landingGet(t, h, "GET", "codr.my", "/en/design"); resp.StatusCode != 301 || resp.Header.Get("Location") != "/design" {
		t.Fatalf("root language at its short code: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if _, body := landingGet(t, h, "GET", "codr.my", "/ms"); !strings.Contains(body, `href="/ms/design"`) {
		t.Fatal("the landing page links the write-up in its language")
	}
	if resp, _ := landingGet(t, h, "GET", "localhost", "/design"); resp.StatusCode == 200 {
		t.Fatal("the write-up is served on the forge host")
	}
}
