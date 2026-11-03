package web

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"

	"codr.my/codr/internal/gitread"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	ghtml "github.com/yuin/goldmark/renderer/html"

	"codr.my/codr/internal/i18n"
)

var (
	markdown = goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(ghtml.WithUnsafe()), // raw HTML passes to the sanitiser below, never to the page
	)
	sanitiser = func() *bluemonday.Policy {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("id").Matching(bluemonday.SpaceSeparatedTokens).OnElements("h1", "h2", "h3", "h4", "h5", "h6")
		p.AllowAttrs("class").Matching(bluemonday.SpaceSeparatedTokens).OnElements("code", "span", "pre")
		// README headers on GitHub centre a logo and title with align="center"; the attribute is presentational, needs no
		// inline style (CSP), and only these three values pass
		// UGC's alt pattern rejects ":" and other ordinary punctuation, so "Screenshot: the merge box" lost its alt text.
		// bluemonday escapes attribute values on output; any plain text up to a sane length is safe here
		p.AllowAttrs("alt", "title").Matching(regexp.MustCompile(`^[^<>\x00-\x1f]{0,500}$`)).OnElements("img")
		p.AllowAttrs("align").Matching(regexp.MustCompile(`^(left|center|right)$`)).OnElements("p", "div", "h1", "h2", "h3", "h4", "h5", "h6")
		p.RequireNoFollowOnLinks(true)
		return p
	}()
)

// mdBase says where a Markdown file lives so its relative links and images resolve inside the
// repo (images → raw/, links → blob/), the way a README on disk would. nil = leave them alone.
type mdBase struct{ Owner, Repo, Ref, Dir string }

// renderMarkdown converts README text to sanitised HTML and reports whether anything was removed.
func renderMarkdown(src []byte, base *mdBase) (template.HTML, bool, error) {
	var buf bytes.Buffer
	if err := markdown.Convert(src, &buf); err != nil {
		return "", false, err
	}
	raw := buf.Bytes()
	clean := sanitiser.SanitizeBytes(raw)
	// the policy ADDS rel="nofollow" to every link: that is not "something removed", so it must not count (before D83
	// every README with a link showed the "HTML removed" note)
	// It also re-escapes characters goldmark leaves bare (' in a code block becomes &#39;), which is not a removal either, so
	// both sides are compared unescaped.
	kept := html.UnescapeString(string(bytes.TrimSpace(bytes.ReplaceAll(clean, []byte(` rel="nofollow"`), nil))))
	removed := kept != html.UnescapeString(string(bytes.TrimSpace(raw)))
	if base != nil {
		clean = resolveRelative(clean, base)
	}
	return template.HTML(clean), removed, nil // #nosec G203 -- sanitised by bluemonday
}

var relAttr = regexp.MustCompile(`\b(src|href)="([^"]*)"`)

// resolveRelative rewrites relative src/href values in sanitised HTML to repo paths. Absolute
// URLs, root-relative paths, fragments and anything that climbs above the repo root are left as is.
func resolveRelative(h []byte, base *mdBase) []byte {
	return relAttr.ReplaceAllFunc(h, func(m []byte) []byte {
		sub := relAttr.FindSubmatch(m)
		attr, val := string(sub[1]), string(sub[2])
		if val == "" || strings.HasPrefix(val, "/") || strings.HasPrefix(val, "#") || strings.HasPrefix(val, "?") || strings.Contains(val, ":") {
			return m
		}
		frag := ""
		if i := strings.IndexByte(val, '#'); i >= 0 {
			val, frag = val[:i], val[i:]
		}
		rel := path.Clean(path.Join(base.Dir, val))
		if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
			return m
		}
		kind := "blob"
		if attr == "src" {
			kind = "raw"
		}
		return []byte(attr + `="/` + base.Owner + "/" + base.Repo + "/" + kind + "/" + base.Ref + "/" + rel + frag + `"`)
	})
}

// codeLine is one highlighted line.
type codeLine struct {
	N    int
	HTML template.HTML
}

// highlight splits text into lines of escaped HTML with chroma's short class names
// (k, s, c1, m…); the CSS maps those to at most four inks. Unknown languages get no spans.
func highlight(name string, text string) []codeLine {
	lexer := lexers.Match(name)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return plainLines(text)
	}
	var out []codeLine
	for i, line := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var b strings.Builder
		for _, tok := range line {
			v := strings.TrimRight(tok.Value, "\n")
			if v == "" {
				continue
			}
			cls := chroma.StandardTypes[tok.Type]
			if cls == "" {
				cls = chroma.StandardTypes[tok.Type.SubCategory()]
			}
			if cls == "" {
				cls = chroma.StandardTypes[tok.Type.Category()]
			}
			if cls == "" || cls == "text" {
				b.WriteString(html.EscapeString(v))
			} else {
				b.WriteString(`<span class="` + cls + `">` + html.EscapeString(v) + `</span>`)
			}
		}
		out = append(out, codeLine{N: i + 1, HTML: template.HTML(b.String())}) // #nosec G203 -- escaped above
	}
	if len(out) > 0 && out[len(out)-1].HTML == "" && strings.HasSuffix(text, "\n") {
		out = out[:len(out)-1]
	}
	return out
}

// diffRow is one diff line with its code highlighted.
type diffRow struct {
	Kind     string
	Old, New int
	HTML     template.HTML
}

// maxDiffHighlight is the line count above which a file's diff is shown without highlighting.
const maxDiffHighlight = 4000

// highlightDiff highlights a file's diff in one lexer pass (D61): the code lines of every hunk are joined
// in order and tokenised once, so a string or comment spanning lines keeps its colour; hunk headers stay text.
func highlightDiff(path string, lines []gitread.Line) []diffRow {
	rows := make([]diffRow, len(lines))
	var code []string
	var at []int
	for i, l := range lines {
		rows[i] = diffRow{Kind: l.Kind, Old: l.Old, New: l.New, HTML: template.HTML(html.EscapeString(l.Text))} // #nosec G203 -- escaped
		if l.Kind != "hunk" {
			code = append(code, l.Text)
			at = append(at, i)
		}
	}
	if len(code) == 0 || len(code) > maxDiffHighlight {
		return rows
	}
	hl := highlight(path, strings.Join(code, "\n")+"\n")
	for len(hl) < len(code) && code[len(hl)] == "" {
		hl = append(hl, codeLine{}) // highlight drops trailing empty lines
	}
	if len(hl) != len(code) {
		return rows // the lexer merged or split lines: keep plain text rather than misalign
	}
	for k, i := range at {
		rows[i].HTML = hl[k].HTML
	}
	return rows
}

// splitCell is one side of a side-by-side diff row; Kind "" = no line on this side.
type splitCell struct {
	N    int
	Kind string // ctx | add | del | ""
	HTML template.HTML
}

// splitRow is a side-by-side diff row: a hunk header, or old on the left and new on the right.
type splitRow struct {
	Hunk bool
	HTML template.HTML
	L, R splitCell
}

// splitDiff pairs a file's diff for the side-by-side view (D63): inside each run of removed then added
// lines the i-th removal sits next to the i-th addition; context lines appear on both sides.
func splitDiff(path string, lines []gitread.Line) []splitRow {
	var out []splitRow
	var dels, adds []diffRow
	flush := func() {
		for i := 0; i < len(dels) || i < len(adds); i++ {
			var r splitRow
			if i < len(dels) {
				r.L = splitCell{N: dels[i].Old, Kind: "del", HTML: dels[i].HTML}
			}
			if i < len(adds) {
				r.R = splitCell{N: adds[i].New, Kind: "add", HTML: adds[i].HTML}
			}
			out = append(out, r)
		}
		dels, adds = nil, nil
	}
	for _, r := range highlightDiff(path, lines) {
		switch r.Kind {
		case "hunk":
			flush()
			out = append(out, splitRow{Hunk: true, HTML: r.HTML})
		case "del":
			if len(adds) > 0 {
				flush()
			}
			dels = append(dels, r)
		case "add":
			adds = append(adds, r)
		default:
			flush()
			out = append(out, splitRow{L: splitCell{N: r.Old, Kind: "ctx", HTML: r.HTML}, R: splitCell{N: r.New, Kind: "ctx", HTML: r.HTML}})
		}
	}
	flush()
	return out
}

func plainLines(text string) []codeLine {
	text = strings.TrimSuffix(text, "\n")
	var out []codeLine
	for i, l := range strings.Split(text, "\n") {
		out = append(out, codeLine{N: i + 1, HTML: template.HTML(html.EscapeString(l))}) // #nosec G203 -- escaped
	}
	return out
}

// relTime says how long ago in the viewer's language; the full timestamp goes in a title.
func relTime(lang string, t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 90*time.Second:
		return i18n.T(lang, "ui.time.now")
	case d < 90*time.Minute:
		return i18n.N(lang, "ui.time.minutes", int(d.Minutes()+0.5))
	case d < 36*time.Hour:
		return i18n.N(lang, "ui.time.hours", int(d.Hours()+0.5))
	case d < 45*24*time.Hour:
		return i18n.N(lang, "ui.time.days", int(d.Hours()/24+0.5))
	case d < 540*24*time.Hour:
		return i18n.N(lang, "ui.time.months", int(d.Hours()/24/30.4+0.5))
	}
	return i18n.N(lang, "ui.time.years", int(d.Hours()/24/365.25+0.5))
}

// humanSize formats bytes with a binary unit; units are not translated (universal symbols).
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	case n < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
}

// crumb is one breadcrumb segment.
type crumb struct {
	Name string
	Path string
}

func crumbs(p string) []crumb {
	if p == "" {
		return nil
	}
	var out []crumb
	acc := ""
	for _, part := range strings.Split(p, "/") {
		acc = path.Join(acc, part)
		out = append(out, crumb{Name: part, Path: acc})
	}
	return out
}
