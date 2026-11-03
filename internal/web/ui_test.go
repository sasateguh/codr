package web

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template/parse"
	"time"

	"golang.org/x/crypto/ssh"

	"codr.my/codr/internal/auth"
	"codr.my/codr/internal/store"
)

// TestTemplatesCarryNoLiteralText: every visible string in templates/*.html must come from
// locale/*.json through $.T (NFR-6). Text nodes may only hold whitespace and punctuation.
func TestTemplatesCarryNoLiteralText(t *testing.T) {
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatal("no templates", err)
	}
	tag := regexp.MustCompile(`<[^>]*>`)
	allowed := regexp.MustCompile(`^[\s·/←→▸—:()+\-−|,.#]*$`)
	for _, f := range files {
		raw, _ := fs.ReadFile(templateFS, f)
		fn := map[string]any{}
		for _, n := range []string{"T", "size", "short", "join", "joinList", "crumbs", "add", "sub", "lines", "rfc", "day", "hasSuffix", "parseTime", "dict", "hue", "initial", "ne", "call", "printf", "len", "eq", "gt", "lt", "not", "and", "or", "slice", "index", "hlDiff", "splitDiff", "notesAt", "askHref"} {
			fn[n] = func() {}
		}
		trees, err := parse.Parse(f, string(raw), "{{", "}}", fn)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for _, tr := range trees {
			// Flatten: text as-is, every action as \x00, so tags split around actions are
			// still recognised as tags once stripped.
			var sb strings.Builder
			var walk func(n parse.Node)
			walk = func(n parse.Node) {
				switch x := n.(type) {
				case *parse.ListNode:
					if x == nil {
						return
					}
					for _, c := range x.Nodes {
						walk(c)
					}
				case *parse.TextNode:
					sb.Write(x.Text)
				case *parse.ActionNode, *parse.TemplateNode:
					sb.WriteByte(0)
				case *parse.IfNode:
					sb.WriteByte(0)
					walk(x.List)
					walk(x.ElseList)
				case *parse.RangeNode:
					sb.WriteByte(0)
					walk(x.List)
					walk(x.ElseList)
				case *parse.WithNode:
					sb.WriteByte(0)
					walk(x.List)
					walk(x.ElseList)
				}
			}
			walk(tr.Root)
			visible := tag.ReplaceAllString(sb.String(), "")
			for _, seg := range strings.Split(visible, "\x00") {
				if !allowed.MatchString(seg) {
					t.Errorf("%s (%s): literal UI text %q — move it to locale/*.json", f, tr.Name, strings.TrimSpace(seg))
				}
			}
		}
	}
	// and no inline style/script: CSP forbids them (D4)
	for _, f := range files {
		raw, _ := fs.ReadFile(templateFS, f)
		if strings.Contains(string(raw), " style=") || strings.Contains(string(raw), "<script>") || strings.Contains(string(raw), "onclick=") {
			t.Errorf("%s: inline style/script found", f)
		}
	}
}

type browser struct {
	t    *testing.T
	h    *harness
	c    *http.Client
	csrf string
	lang string // Accept-Language sent with every POST
}

func (h *harness) browser() *browser {
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &browser{t: h.t, h: h, c: c}
}

func (b *browser) get(path, lang string) (int, string) {
	b.t.Helper()
	req, _ := http.NewRequest("GET", b.h.srv.URL+path, nil)
	if lang != "" {
		req.Header.Set("Accept-Language", lang)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	noFormatErrors(b.t, path, body)
	for _, ck := range b.c.Jar.Cookies(mustURL(b.h.srv.URL)) {
		if ck.Name == cookieCSRF {
			b.csrf = ck.Value
		}
	}
	return resp.StatusCode, string(body)
}

func (b *browser) post(path string, form url.Values) (int, string, string) {
	b.t.Helper()
	if form.Get("csrf") == "" && b.csrf != "" {
		form.Set("csrf", b.csrf)
	}
	req, _ := http.NewRequest("POST", b.h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if b.lang != "" {
		req.Header.Set("Accept-Language", b.lang)
	}
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	noFormatErrors(b.t, path, body)
	return resp.StatusCode, string(body), resp.Header.Get("Location")
}

// noFormatErrors fails a test whose page shows a fmt error ("%!(EXTRA", "%!d(MISSING)"): a message given the wrong
// arguments. Every page any web test reads is checked, so a new template cannot ship one (v2.14.0 did, on a count).
func noFormatErrors(t *testing.T, path string, body []byte) {
	t.Helper()
	if i := bytes.Index(body, []byte("%!")); i >= 0 {
		end := min(i+40, len(body))
		t.Errorf("%s: format error in page: %q", path, body[i:end])
	}
}

func mustURL(s string) *url.URL { u, _ := url.Parse(s); return u }

func (h *harness) seed(t *testing.T) (pub, priv store.Repo) {
	t.Helper()
	ctx := context.Background()
	hash, _ := auth.Hash("kata laluan alice", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err := h.st.SetPasswordHash(ctx, h.users["alice"].ID, hash); err != nil {
		t.Fatal(err)
	}
	pub = h.repo("alice", "awam", false)
	priv = h.repo("alice", "rahsia", true)
	w := h.work()
	_ = os.MkdirAll(filepath.Join(w, "src"), 0o700)
	_ = os.WriteFile(filepath.Join(w, "README.md"), []byte("# Projek awam\n\nHalo *dunia* <script>alert(1)</script>\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"), 0o600)
	_ = os.WriteFile(filepath.Join(w, "src", "main.go"), []byte("package main\n\n// komen\nfunc main() { println(\"hai\") }\n"), 0o600)
	_ = os.WriteFile(filepath.Join(w, "logo.bin"), []byte{0, 1, 2, 0xff}, 0o600)
	h.commit(w, "nota.txt", "satu\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	h.commit(w, "nota.txt", "dua\n")
	if out, err := h.git(w, "push", "-q", h.url("alice/awam", "alice"), "main", "main:refs/tags/v1"); err != nil {
		t.Fatal(out)
	}
	if out, err := h.git(w, "push", "-q", h.url("alice/rahsia", "alice"), "main"); err != nil {
		t.Fatal(out)
	}
	return pub, priv
}

func TestUIAnonymousSeesOnlyPublic(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	code, body := b.get("/", "ms")
	if code != 200 || !strings.Contains(body, `href="/alice/awam"`) || strings.Contains(body, "rahsia") {
		t.Fatalf("home: %d %s", code, body)
	}
	if !strings.Contains(body, `lang="ms-MY"`) || !strings.Contains(body, "Log masuk") {
		t.Fatal("home not in Malay for Accept-Language ms")
	}
	if code, body := b.get("/alice/rahsia", "en"); code != 404 || !strings.Contains(body, "Nothing here") {
		t.Fatalf("private repo for anonymous: %d", code)
	}
	if code, _ := b.get("/alice/rahsia/commits/main", ""); code != 404 {
		t.Fatal("private log leaked")
	}
	if code, _ := b.get("/nobody", ""); code != 404 {
		t.Fatal("unknown owner")
	}
	if code, _ := b.get("/admin/tetapan", ""); code != 303 {
		t.Fatal("settings must redirect anonymous to login")
	}
	code, body = b.get("/alice/awam", "en")
	if code != 200 {
		t.Fatalf("public repo: %d", code)
	}
	for _, want := range []string{"Projek awam", "<em>dunia</em>", "src/", "README.md", "ssh://git@localhost:2222/alice/awam.git", "Latest commit", "c nota.txt", "<table>"} {
		if !strings.Contains(body, want) {
			t.Errorf("repo page lacks %q", want)
		}
	}
	// the About panel measures the code: Go is the only language (README/notes/binary do not count) (D67)
	for _, want := range []string{`class="tentang kad"`, "<span>Go</span>", "100.0%", `class="bar-bahasa"`} {
		if !strings.Contains(body, want) {
			t.Errorf("about panel lacks %q", want)
		}
	}
	// every file-list row carries its last commit, linked to that commit (D61)
	if !strings.Contains(body, `class="baris-meta pokok-commit" href="/alice/awam/commit/`) {
		t.Error("file list lacks the per-entry last commit")
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("README script not sanitised")
	}
	if !strings.Contains(body, "Some HTML in the README was removed") {
		t.Fatal("sanitised note missing")
	}
	// CSP + security headers on HTML
	req, _ := http.NewRequest("GET", h.srv.URL+"/alice/awam", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("headers: %v", resp.Header)
	}
}

func TestUIPagesAndRaw(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	code, body := b.get("/alice/awam/blob/main/src/main.go", "en")
	if code != 200 || !strings.Contains(body, `<span class="c1">// komen</span>`) || !strings.Contains(body, "4 lines") {
		t.Fatalf("blob: %d %s", code, body)
	}
	if code, body := b.get("/alice/awam/blob/main/README.md", "en"); code != 200 || !strings.Contains(body, "<em>dunia</em>") {
		t.Fatalf("markdown blob: %d", code)
	}
	if code, body := b.get("/alice/awam/blob/main/README.md?papar=kod", "en"); code != 200 || strings.Contains(body, "<em>dunia</em>") || !strings.Contains(body, "dunia") {
		t.Fatalf("markdown as code: %d", code)
	}
	if code, body := b.get("/alice/awam/blob/main/logo.bin", "en"); code != 200 || !strings.Contains(body, "Binary file, 4 B") {
		t.Fatalf("binary blob: %d", code)
	}
	if code, body := b.get("/alice/awam/tree/main/src", "en"); code != 200 || !strings.Contains(body, `href="/alice/awam/blob/main/src/main.go"`) {
		t.Fatalf("tree: %d", code)
	}
	if code, body := b.get("/alice/awam/tree/v1", "en"); code != 200 || !strings.Contains(body, `aria-current="true">v1`) {
		t.Fatalf("tag tree: %d", code)
	}
	code, body = b.get("/alice/awam/commits/main", "en")
	if code != 200 || strings.Count(body, `class="baris"`) != 2 || !strings.Contains(body, "Commits on main") {
		t.Fatalf("log: %d %s", code, body)
	}
	hash := regexp.MustCompile(`/alice/awam/commit/([0-9a-f]{40})`).FindStringSubmatch(body)
	if hash == nil {
		t.Fatal("no commit link")
	}
	code, body = b.get("/alice/awam/commit/"+hash[1], "en")
	if code != 200 || !strings.Contains(body, "1 file changed") || !strings.Contains(body, `class="baris-kod tambah"`) || !strings.Contains(body, "nota.txt") {
		t.Fatalf("commit: %d %s", code, body)
	}
	if code, body := b.get("/alice/awam/commit/"+hash[1][:7], "en"); code != 200 || !strings.Contains(body, hash[1]) {
		t.Fatal("short hash commit page")
	}
	if code, body := b.get("/alice/awam/refs", "en"); code != 200 || !strings.Contains(body, ">default<") || !strings.Contains(body, ">v1<") {
		t.Fatalf("refs: %d %s", code, body)
	}
	// raw: text inline as text/plain, binary as a download, never html
	resp, _ := http.Get(h.srv.URL + "/alice/awam/raw/main/README.md")
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.Contains(string(raw), "<script>") {
		t.Fatalf("raw text: %d %s", resp.StatusCode, resp.Header)
	}
	resp, _ = http.Get(h.srv.URL + "/alice/awam/raw/main/logo.bin")
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("raw binary headers: %v", resp.Header)
	}
	// bad refs and paths are 404, never 500
	for _, p := range []string{"/alice/awam/tree/nope", "/alice/awam/blob/main/nope.txt", "/alice/awam/commit/zzz", "/alice/awam/blob/--output=x/README.md", "/alice/awam/raw/main/src", "/alice/awam/tree/main/src%2F..%2F..%2Fetc"} {
		if code, _ := b.get(p, "en"); code != 404 {
			t.Errorf("%s: %d, want 404", p, code)
		}
	}
	// ServeMux cleans ".." itself and answers with a redirect to the clean path, never content
	if code, _ := b.get("/alice/awam/tree/main/../../etc", "en"); code != 307 && code != 404 {
		t.Errorf("dot-dot path: %d", code)
	}
	if code, _ := b.get("/alice/awam.git", "en"); code != 301 {
		t.Fatal(".git page should redirect")
	}
	if code, body := b.get("/?q=aw", "en"); code != 200 || !strings.Contains(body, "/alice/awam") || !strings.Contains(body, "1 repo") {
		t.Fatalf("search: %d", code)
	}
	if code, body := b.get("/?q=zzz", "en"); code != 200 || !strings.Contains(body, "No repo matches") {
		t.Fatalf("empty search: %d", code)
	}
}

func TestUILoginSessionSettings(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	b.lang = "en"
	if code, body := b.get("/login", "en"); code != 200 || !strings.Contains(body, `name="csrf"`) || b.csrf == "" {
		t.Fatalf("login form: %d", code)
	}
	// no csrf → 400; wrong password → 401 + Malay message when asked
	if code, _, _ := b.post("/login", url.Values{"user": {"alice"}, "password": {"x"}, "csrf": {"bad"}}); code != 400 {
		t.Fatalf("csrf: %d", code)
	}
	if code, body, _ := b.post("/login", url.Values{"user": {"alice"}, "password": {"salah"}}); code != 401 || !strings.Contains(body, "Wrong username or password") {
		t.Fatalf("wrong password: %d", code)
	}
	if code, _, _ := b.post("/login", url.Values{"user": {"nobody"}, "password": {"salah"}}); code != 401 {
		t.Fatal("unknown user must look like a wrong password")
	}
	code, _, loc := b.post("/login", url.Values{"user": {"Alice"}, "password": {"kata laluan alice"}, "next": {"/alice/rahsia"}})
	if code != 303 || loc != "/alice/rahsia" {
		t.Fatalf("login: %d %s", code, loc)
	}
	if code, body := b.get("/alice/rahsia", "en"); code != 200 || !strings.Contains(body, "private") {
		t.Fatalf("private repo for owner: %d", code)
	}
	if code, body := b.get("/", "en"); code != 200 || !strings.Contains(body, "rahsia") || !strings.Contains(body, "Log out") || !strings.Contains(body, `<details class="menu-pengguna">`) || !strings.Contains(body, `class="papan"`) || !strings.Contains(body, "All repositories (2)") || !strings.Contains(body, `<ul class="sisi-repo">
    <li><a href="/alice/`) || !strings.Contains(body, "pushed to alice/rahsia") {
		t.Fatalf("home logged in (dashboard, D68): %d", code)
	}
	if code, body := b.get("/?semua=1", "en"); code != 200 || !strings.Contains(body, "2 repos") || strings.Contains(body, `class="papan"`) {
		t.Fatalf("full list: %d", code)
	}
	// open redirect refused
	if code, _, loc := b.post("/lang", url.Values{"lang": {"ms"}, "next": {"//evil.example/x"}}); code != 303 || loc != "/" {
		t.Fatalf("lang/open redirect: %d %s", code, loc)
	}
	if code, body := b.get("/alice/awam", "en"); code != 200 || !strings.Contains(body, `lang="ms-MY"`) {
		t.Fatal("language cookie not applied")
	}
	// back to English for the settings checks (the cookie wins over Accept-Language)
	if code, _, _ := b.post("/lang", url.Values{"lang": {"en-US"}, "next": {"/"}}); code != 303 {
		t.Fatal("lang back to en")
	}
	// settings: alice is not admin → 403; make her admin → page + save + bad value
	if code, _ := b.get("/admin/tetapan", ""); code != 403 {
		t.Fatalf("settings as non-admin: %d", code)
	}
	if _, err := h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	if code, body := b.get("/admin/tetapan", "en"); code != 200 || !strings.Contains(body, "repo.push_max_mb") {
		t.Fatalf("settings page: %d", code)
	}
	if code, _, _ := b.post("/admin/tetapan", url.Values{"key": {"repo.push_max_mb"}, "value": {"7"}}); code != 303 {
		t.Fatal("save")
	}
	if v, _ := h.st.SettingInt(context.Background(), "repo.push_max_mb"); v != 7 {
		t.Fatalf("setting not saved: %d", v)
	}
	if code, body := b.get("/admin/tetapan", "en"); !strings.Contains(body, "Setting repo.push_max_mb saved") || code != 200 {
		t.Fatal("flash missing")
	}
	b.post("/admin/tetapan", url.Values{"key": {"repo.push_max_mb"}, "value": {"banyak"}})
	if _, body := b.get("/admin/tetapan", "en"); !strings.Contains(body, "must be of type int") {
		t.Fatal("bad value flash missing")
	}
	if code, _, _ := b.post("/admin/tetapan", url.Values{"key": {"site.name"}, "value": {"Forge Aiman"}}); code != 303 {
		t.Fatal("save name")
	}
	if _, body := b.get("/", "en"); !strings.Contains(body, `<span class="wordmark">Forge Aiman</span>`) {
		t.Fatal("site name not read from settings")
	}
	// logout kills the session server-side
	var tok string
	for _, ck := range b.c.Jar.Cookies(mustURL(h.srv.URL)) {
		if ck.Name == cookieSession {
			tok = ck.Value
		}
	}
	if code, _, _ := b.post("/logout", url.Values{}); code != 303 {
		t.Fatal("logout")
	}
	if _, err := h.st.SessionUser(context.Background(), tok); err == nil {
		t.Fatal("session survived logout")
	}
	if code, _ := b.get("/alice/rahsia", "en"); code != 404 {
		t.Fatal("still sees private after logout")
	}
}

func TestUIStaticFingerprint(t *testing.T) {
	h := newHarness(t)
	if resp, err := http.Get(h.srv.URL + "/login"); err == nil {
		resp.Body.Close()
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") || !strings.Contains(cc, "no-transform") {
			t.Fatalf("html Cache-Control %q: want no-store + no-transform (Cloudflare injects its beacon otherwise)", cc)
		}
	}
	_, body := h.browser().get("/login", "")
	m := regexp.MustCompile(`/static/([0-9a-f]{8})/codr\.css`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no fingerprinted css link")
	}
	resp, _ := http.Get(h.srv.URL + "/static/" + m[1] + "/codr.css")
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "must-revalidate") || !strings.Contains(resp.Header.Get("Cache-Control"), "no-transform") || resp.Header.Get("ETag") == "" {
		t.Fatalf("css: %d %s etag=%q (WebKit needs revalidation, D37)", resp.StatusCode, resp.Header.Get("Cache-Control"), resp.Header.Get("ETag"))
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/static/"+m[1]+"/codr.css", nil)
	req.Header.Set("If-None-Match", resp.Header.Get("ETag"))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional request: %d, want 304", resp.StatusCode)
	}
	for _, f := range []string{"/fonts/ibm-plex-mono-400.woff2", "/logo.svg", "/tema.js", "/og.png", "/apple-touch-icon.png"} {
		resp, _ = http.Get(h.srv.URL + "/static/" + m[1] + f)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s not served: %d", f, resp.StatusCode)
		}
	}
	if !strings.Contains(body, `<meta property="og:image" content="`+h.s.UI.HTTPHost+"/static/"+m[1]+`/og.png">`) {
		t.Fatal("og:image must be absolute (http_host + fingerprinted path)")
	}
	if !strings.Contains(body, "/static/"+m[1]+"/tema.js") || !strings.Contains(body, `rel="apple-touch-icon"`) {
		t.Fatal("head must load tema.js and the touch icon")
	}
}

// TestCSSDarkBlocksIdentical: the dark tokens exist twice in amcd.css (system dark without a choice,
// and the explicit data-tema="dark" choice). Both blocks are marked; they must carry the same rules.
func TestCSSDarkBlocksIdentical(t *testing.T) {
	css, err := fs.ReadFile(staticFS, "static/codr.css")
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	rest := string(css)
	for {
		i := strings.Index(rest, "/* tema-gelap mula")
		if i < 0 {
			break
		}
		rest = rest[i:]
		j := strings.Index(rest, "/* tema-gelap tamat */")
		if j < 0 {
			t.Fatal("unterminated dark block")
		}
		blocks = append(blocks, rest[:j])
		rest = rest[j+1:]
	}
	if len(blocks) != 2 {
		t.Fatalf("want 2 dark blocks, got %d", len(blocks))
	}
	norm := func(b string) string {
		b = b[strings.Index(b, "*/")+2:]
		b = strings.ReplaceAll(b, "@media (prefers-color-scheme: dark) {", "")
		b = strings.ReplaceAll(b, `:root:not([data-tema="light"])`, "SEL")
		b = strings.ReplaceAll(b, `:root[data-tema="dark"]`, "SEL")
		return regexp.MustCompile(`\s+`).ReplaceAllString(strings.TrimSpace(b), " ")
	}
	a, b := norm(blocks[0]), norm(blocks[1])
	// the media block has one extra closing brace
	a = strings.TrimSuffix(strings.TrimSpace(a), "}")
	if strings.TrimSpace(a) != strings.TrimSpace(b) {
		t.Fatalf("dark blocks differ:\n%s\n---\n%s", a, b)
	}
}

func TestUICreateRepoAndMirrorAndAudit(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	b.lang = "en"
	if code, _, loc := b.post("/baharu", url.Values{"name": {"x"}}); code != 303 || loc != "/login?next=/baharu" {
		t.Fatalf("anonymous create: %d %s", code, loc)
	}
	b.get("/login", "en")
	b.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}})
	if code, body := b.get("/baharu", "en"); code != 200 || !strings.Contains(body, `name="url"`) {
		t.Fatalf("form: %d", code)
	}
	if code, body, _ := b.post("/baharu", url.Values{"name": {"Bad Name"}}); code != 400 || !strings.Contains(body, "Invalid repo name") {
		t.Fatalf("bad name: %d", code)
	}
	if code, body, _ := b.post("/baharu", url.Values{"name": {"awam"}}); code != 409 || !strings.Contains(body, "already have") {
		t.Fatalf("duplicate: %d", code)
	}
	if code, body, _ := b.post("/baharu", url.Values{"name": {"m"}, "url": {"file:///etc"}}); code != 400 || !strings.Contains(body, "Invalid mirror URL") {
		t.Fatalf("local url via web must be refused: %d", code)
	}
	code, _, loc := b.post("/baharu", url.Values{"name": {"baru"}, "desc": {"projek baharu"}, "visibility": {"public"}})
	if code != 303 || loc != "/alice/baru" {
		t.Fatalf("create: %d %s", code, loc)
	}
	if code, body := b.get("/alice/baru", "en"); code != 200 || !strings.Contains(body, "Repo alice/baru created") || !strings.Contains(body, "still empty") || !strings.Contains(body, ">public<") {
		t.Fatalf("new repo page: %d", code)
	}
	// mirror of this very server's public repo over http (the only network source in a test)
	code, _, loc = b.post("/baharu", url.Values{"name": {"salinan"}, "url": {h.srv.URL + "/alice/awam.git"}})
	if code != 303 || loc != "/alice/salinan" {
		t.Fatalf("create mirror: %d %s", code, loc)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		m, err := h.st.MirrorOf(context.Background(), mustRepo(t, h, "alice", "salinan").ID)
		if err == nil && m.LastSyncAt != "" {
			if m.LastError != "" {
				t.Fatalf("mirror sync error: %s", m.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first sync never ran")
		}
		time.Sleep(100 * time.Millisecond)
	}
	code, body := b.get("/alice/salinan", "en")
	if code != 200 || !strings.Contains(body, ">mirror<") || !strings.Contains(body, "Mirror of "+h.srv.URL) || !strings.Contains(body, "fresh just now") || !strings.Contains(body, "Projek awam") {
		t.Fatalf("mirror page: %d %s", code, body)
	}
	// pushes to a mirror are refused at the HTTP door, even for the owner
	if code, body := h.get("/alice/salinan.git/info/refs?service=git-receive-pack", "Bearer "+h.tokens["alice"], "en"); code != 403 || !strings.Contains(body, "is a mirror") {
		t.Fatalf("push to mirror: %d %s", code, body)
	}
	// audit: admin only; rows carry the create + import
	if code, _ := b.get("/admin/audit", "en"); code != 403 {
		t.Fatal("audit for non-admin")
	}
	if _, err := h.st.DB().Exec(`UPDATE users SET is_admin=1 WHERE name='alice'`); err != nil {
		t.Fatal(err)
	}
	code, body = b.get("/admin/audit", "en")
	if code != 200 || !strings.Contains(body, "repo.import") || !strings.Contains(body, "alice/salinan") || !strings.Contains(body, "mirror.sync") {
		t.Fatalf("audit: %d %s", code, body)
	}
}

func mustRepo(t *testing.T, h *harness, owner, name string) store.Repo {
	t.Helper()
	r, err := h.st.RepoByPath(context.Background(), owner, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestUIAccountKeysAndTokens: a signed-in user manages their own SSH keys and HTTPS tokens
// at /akaun (v0.6); the raw token is shown once in the response, never in a cookie.
func TestUIAccountKeysAndTokens(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	b.lang = "en"
	if code, _ := b.get("/akaun", "en"); code != 303 { // anonymous: sent to login
		t.Fatalf("anonymous /akaun: %d", code)
	}
	b.get("/login", "en")
	if code, _, _ := b.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 303 {
		t.Fatal("login")
	}
	if code, body := b.get("/akaun", "en"); code != 200 || !strings.Contains(body, "No SSH keys yet") || !strings.Contains(body, "ssh://git@") || !strings.Contains(body, `name="scope" value="repo:read"`) || strings.Contains(body, `value="admin"`) {
		t.Fatalf("account page (harness seeds alice a token, so the token table is present; admin scope must not be offered): %d", code)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if code, body, _ := b.post("/akaun/kunci", url.Values{"title": {"laptop"}, "key": {"not a key"}}); code != 400 || !strings.Contains(body, "Invalid public key") {
		t.Fatalf("bad key: %d", code)
	}
	if code, _, loc := b.post("/akaun/kunci", url.Values{"title": {"laptop"}, "key": {authorized}}); code != 303 || loc != "/akaun" {
		t.Fatalf("add key: %d %s", code, loc)
	}
	if code, body := b.get("/akaun", "en"); code != 200 || !strings.Contains(body, "SHA256:") || !strings.Contains(body, "laptop") || !strings.Contains(body, "SSH key SHA256:") {
		t.Fatalf("key listed + flash: %d", code)
	}
	if code, body, _ := b.post("/akaun/kunci", url.Values{"key": {authorized}}); code != 400 || !strings.Contains(body, "already registered") {
		t.Fatalf("duplicate key: %d", code)
	}
	keys, _ := h.st.SSHKeys(context.Background(), h.users["alice"].ID)
	if len(keys) != 1 {
		t.Fatalf("keys: %d", len(keys))
	}
	// bob cannot remove alice's key through his own session (ids are per user)
	bobHash, _ := auth.Hash("kata laluan bob", auth.Params{Time: 1, Memory: 8 * 1024, Threads: 1})
	if err := h.st.SetPasswordHash(context.Background(), h.users["bob"].ID, bobHash); err != nil {
		t.Fatal(err)
	}
	bb := h.browser()
	bb.get("/login", "en")
	bb.post("/login", url.Values{"user": {"bob"}, "password": {"kata laluan bob"}})
	bb.get("/akaun", "en")
	bb.post("/akaun/kunci/padam", url.Values{"id": {strconv.FormatInt(keys[0].ID, 10)}})
	if k, _ := h.st.SSHKeys(context.Background(), h.users["alice"].ID); len(k) != 1 {
		t.Fatal("bob removed alice's key")
	}
	if code, _, _ := b.post("/akaun/kunci/padam", url.Values{"id": {strconv.FormatInt(keys[0].ID, 10)}}); code != 303 {
		t.Fatal("remove key")
	}
	if k, _ := h.st.SSHKeys(context.Background(), h.users["alice"].ID); len(k) != 0 {
		t.Fatal("key not removed")
	}
	// tokens: no scope → 400; too many days → 400; admin scope ignored for a non-admin; raw shown once
	if code, body, _ := b.post("/akaun/token", url.Values{"name": {"ci"}, "days": {"7"}}); code != 400 || !strings.Contains(body, "at least one scope") {
		t.Fatalf("no scope: %d", code)
	}
	if code, body, _ := b.post("/akaun/token", url.Values{"name": {"ci"}, "scope": {"repo:read"}, "days": {"99999"}}); code != 400 || !strings.Contains(body, "between 1 and") {
		t.Fatalf("bad days: %d", code)
	}
	code, body, _ := b.post("/akaun/token", url.Values{"name": {"ci"}, "scope": {"repo:read", "admin"}, "days": {"7"}})
	raw := regexp.MustCompile(`codr_[0-9a-f]{64}`).FindString(body)
	if code != 200 || raw == "" || !strings.Contains(body, "Copy it now") {
		t.Fatalf("create token: %d", code)
	}
	tok, u, err := h.st.TokenUser(context.Background(), raw)
	if err != nil || u.Name != "alice" || strings.Join(tok.Scopes, ",") != "repo:read" {
		t.Fatalf("token: %v %v %v", tok.Scopes, u.Name, err)
	}
	if code, body := b.get("/akaun", "en"); code != 200 || !strings.Contains(body, "ci") || strings.Contains(body, raw) {
		t.Fatalf("token listed without the raw value: %d", code)
	}
	if code, _, _ := b.post("/akaun/token/padam", url.Values{"id": {strconv.FormatInt(tok.ID, 10)}}); code != 303 {
		t.Fatal("revoke")
	}
	if _, _, err := h.st.TokenUser(context.Background(), raw); err == nil {
		t.Fatal("token still valid after revoke")
	}
	if _, body := b.get("/akaun", "en"); !strings.Contains(body, "revoked") {
		t.Fatal("revoke flash missing")
	}
	// password change (FR-01): wrong current, too short, mismatch, then success + login with the new one
	pw := func(cur, next, again string) (int, string) {
		code, body, _ := b.post("/akaun/katalaluan", url.Values{"current": {cur}, "password": {next}, "again": {again}})
		return code, body
	}
	if code, body := pw("salah", "kata laluan baharu panjang", "kata laluan baharu panjang"); code != 400 || !strings.Contains(body, "current password is wrong") {
		t.Fatalf("wrong current: %d", code)
	}
	if code, body := pw("kata laluan alice", "pendek", "pendek"); code != 400 || !strings.Contains(body, "too short") {
		t.Fatalf("short: %d", code)
	}
	if code, body := pw("kata laluan alice", "kata laluan baharu panjang", "kata laluan baharu lain"); code != 400 || !strings.Contains(body, "do not match") {
		t.Fatalf("mismatch: %d", code)
	}
	if code, _, loc := b.post("/akaun/katalaluan", url.Values{"current": {"kata laluan alice"}, "password": {"kata laluan baharu panjang"}, "again": {"kata laluan baharu panjang"}}); code != 303 || !strings.HasPrefix(loc, "/akaun") {
		t.Fatalf("change: %d %s", code, loc)
	}
	if _, body := b.get("/akaun", "en"); !strings.Contains(body, "Password changed") {
		t.Fatal("password flash missing")
	}
	b2 := h.browser()
	b2.get("/login", "en")
	if code, _, _ := b2.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan alice"}}); code != 401 {
		t.Fatal("old password still accepted")
	}
	if code, _, _ := b2.post("/login", url.Values{"user": {"alice"}, "password": {"kata laluan baharu panjang"}}); code != 303 {
		t.Fatal("new password rejected")
	}
}

// TestUIBlame: the blame page pairs each line with its commit; binary files and bad paths
// are refused the same way as the file page; private repos stay private.
func TestUIBlame(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	code, body := b.get("/alice/awam/blame/main/README.md", "en")
	if code != 200 || !strings.Contains(body, `class="kotak-kod blame"`) || !strings.Contains(body, "/alice/awam/commit/") || !strings.Contains(body, `id="L1"`) {
		t.Fatalf("blame page: %d", code)
	}
	if code, _ := b.get("/alice/awam/blame/main/tiada.txt", "en"); code != 404 {
		t.Fatalf("missing file: %d", code)
	}
	if code, _ := b.get("/alice/awam/blame/main/../x", "en"); code == 200 {
		t.Fatalf("bad path: %d", code)
	}
	if code, _ := b.get("/alice/rahsia/blame/main/README.md", "en"); code != 404 {
		t.Fatalf("private repo blame for anonymous: %d", code)
	}
	if code, body := b.get("/alice/awam/blob/main/README.md", "en"); code != 200 || !strings.Contains(body, "/alice/awam/blame/main/README.md") {
		t.Fatal("file page lacks the blame button")
	}
}

// TestUISearch: in-repo search by file name and content, limits from settings, private stays private.
func TestUISearch(t *testing.T) {
	h := newHarness(t)
	h.seed(t)
	b := h.browser()
	if code, body := b.get("/alice/awam/cari", "en"); code != 200 || !strings.Contains(body, `name="q"`) || !strings.Contains(body, "Searches file names") {
		t.Fatalf("empty search page: %d", code)
	}
	if code, body := b.get("/alice/awam/cari?q=x", "en"); code != 200 || !strings.Contains(body, "at least 2") {
		t.Fatalf("short query: %d", code)
	}
	code, body := b.get("/alice/awam/cari?q=readme", "en")
	if code != 200 || !strings.Contains(body, "Matching files (1)") || !strings.Contains(body, "/alice/awam/blob/main/README.md") {
		t.Fatalf("file-name search: %d", code)
	}
	if code, body := b.get("/alice/awam/cari?q=HALO", "en"); code != 200 || !strings.Contains(body, "Matching lines (") || !strings.Contains(body, "README.md#L") {
		t.Fatalf("content search: %d", code)
	}
	if code, body := b.get("/alice/awam/cari?q=tiada-langsung-xyz", "en"); code != 200 || !strings.Contains(body, "No matches for") {
		t.Fatalf("no match: %d", code)
	}
	if code, body := b.get("/alice/awam/cari?q=%3Cscript%3E", "en"); code != 200 || strings.Contains(body, "<script>") {
		t.Fatal("query must be escaped")
	}
	if code, _ := b.get("/alice/rahsia/cari?q=halo", "en"); code != 404 {
		t.Fatalf("private repo search for anonymous: %d", code)
	}
	if code, _ := b.get("/alice/awam/cari?q=halo&ref=-x", "en"); code == 200 {
		t.Fatal("bad ref must be refused")
	}
}
