package web

import (
	"strings"
	"testing"

	"codr.my/codr/internal/gitread"
)

// TestHighlightDiff: one lexer pass over a file's diff keeps every row aligned, colours code, escapes
// HTML, and leaves hunk headers as plain text (D61).
func TestHighlightDiff(t *testing.T) {
	lines := []gitread.Line{
		{Kind: "hunk", Text: "@@ -1,2 +1,3 @@ func main() {"},
		{Kind: "ctx", Old: 1, New: 1, Text: "package main"},
		{Kind: "del", Old: 2, Text: `var s = "<b>lama</b>"`},
		{Kind: "add", New: 2, Text: `var s = "baharu"`},
		{Kind: "add", New: 3, Text: ""},
	}
	rows := highlightDiff("main.go", lines)
	if len(rows) != len(lines) {
		t.Fatalf("rows %d, lines %d", len(rows), len(lines))
	}
	if rows[0].Kind != "hunk" || strings.Contains(string(rows[0].HTML), "<span") {
		t.Fatalf("hunk header: %q", rows[0].HTML)
	}
	if !strings.Contains(string(rows[1].HTML), `<span class="k`) || !strings.Contains(string(rows[3].HTML), `<span class="s`) {
		t.Fatalf("not highlighted: %q %q", rows[1].HTML, rows[3].HTML)
	}
	if strings.Contains(string(rows[2].HTML), "<b>") || rows[2].Old != 2 || rows[3].New != 2 {
		t.Fatalf("escaping or numbers wrong: %+v", rows[2])
	}
	if plain := highlightDiff("x.unknownext", lines[1:2]); len(plain) != 1 {
		t.Fatal("unknown language must still give one row per line")
	}
}

// TestSplitDiff: removals pair with the additions that follow them, the longer side leaves blanks,
// context sits on both sides and hunk headers stay whole rows (D63).
func TestSplitDiff(t *testing.T) {
	rows := splitDiff("a.txt", []gitread.Line{
		{Kind: "hunk", Text: "@@ -1,3 +1,3 @@"},
		{Kind: "ctx", Old: 1, New: 1, Text: "sama"},
		{Kind: "del", Old: 2, Text: "lama 1"},
		{Kind: "del", Old: 3, Text: "lama 2"},
		{Kind: "add", New: 2, Text: "baharu"},
		{Kind: "add", New: 3, Text: "lagi"},
		{Kind: "add", New: 4, Text: "tambahan"},
	})
	if len(rows) != 5 || !rows[0].Hunk {
		t.Fatalf("rows: %+v", rows)
	}
	if rows[1].L.Kind != "ctx" || rows[1].R.Kind != "ctx" || rows[1].L.N != 1 || rows[1].R.N != 1 {
		t.Fatalf("context: %+v", rows[1])
	}
	if rows[2].L.N != 2 || rows[2].R.N != 2 || rows[3].L.N != 3 || rows[3].R.N != 3 {
		t.Fatalf("pairs: %+v %+v", rows[2], rows[3])
	}
	if rows[4].L.Kind != "" || rows[4].R.Kind != "add" || rows[4].R.N != 4 {
		t.Fatalf("extra addition: %+v", rows[4])
	}
}

// Ordinary Markdown is not reported as "HTML removed"; only something the sanitiser actually took out is.
func TestMarkdownRemovedOnlyWhenRemoved(t *testing.T) {
	plain := "# Tajuk\n\nTeks *miring* dan **tebal**, [pautan](https://contoh.invalid) dan <https://contoh.invalid/auto>.\n\n" +
		"- satu\n- dua\n\n1. a\n2. b\n\n> petikan\n\n```go\nfunc main() {}\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n![gambar](logo.png)\n\n---\n\n`kod` dan ~~coret~~\n"
	if _, removed, err := renderMarkdown([]byte(plain), nil); err != nil || removed {
		t.Fatalf("plain Markdown reported as sanitised: %v", err)
	}
	// a GitHub-style centred README header keeps its alignment and is not reported as sanitised
	header := "<p align=\"center\"><img src=\"logo.png\" width=\"112\" height=\"112\" alt=\"Logo\"></p>\n\n<h1 align=\"center\">Nama</h1>\n"
	if h, removed, _ := renderMarkdown([]byte(header), nil); removed || !strings.Contains(string(h), `<p align="center">`) || !strings.Contains(string(h), `<h1 align="center">`) || !strings.Contains(string(h), `width="112"`) {
		t.Fatalf("centred header: removed=%v html=%s", removed, h)
	}
	// quotes and ampersands that the sanitiser merely re-escapes are not "removed"
	if _, removed, _ := renderMarkdown([]byte("```sh\nprintf 'a & b\\n' | grep \"x\"\n```\n\nIt's 100% & \"done\".\n"), nil); removed {
		t.Fatal("escaping alone reported as sanitised")
	}
	// alt text keeps ordinary punctuation (it is what a screen reader reads); markup in it is still refused
	if h, removed, _ := renderMarkdown([]byte("<img src=\"a.png\" alt=\"Screenshot: the merge box, 100% (v2)\">\n"), nil); removed || !strings.Contains(string(h), `alt="Screenshot: the merge box, 100% (v2)"`) {
		t.Fatalf("alt with punctuation: removed=%v html=%s", removed, h)
	}
	if h, _, _ := renderMarkdown([]byte("<img src=\"a.png\" alt=\"x&quot; onerror=&quot;alert(1)\">\n"), nil); strings.Contains(string(h), `onerror="`) {
		t.Fatalf("alt broke out of its attribute: %s", h)
	}
	if h, removed, _ := renderMarkdown([]byte("<p align=\"javascript:x\" onclick=\"x()\">a</p>\n"), nil); !removed || strings.Contains(string(h), "align=") || strings.Contains(string(h), "onclick") {
		t.Fatalf("align takes only left|center|right: %s", h)
	}
	for _, bad := range []string{"<script>alert(1)</script>", "<img src=x onerror=alert(1)>", "[x](javascript:alert(1))", "<iframe src=//x></iframe>"} {
		if h, removed, _ := renderMarkdown([]byte("teks\n\n"+bad+"\n"), nil); !removed || strings.Contains(string(h), "alert(1)") && strings.Contains(string(h), "<script") {
			t.Errorf("%q: removed=%v html=%s", bad, removed, h)
		}
	}
}
