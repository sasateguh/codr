// Package xref finds references between issues, commits and pull requests (D87, FR-21): "owner/repo#n" links, and
// "fixes #n" style keywords that close an issue once the commit reaches the default branch or the pull request merges.
package xref

import (
	"regexp"
	"strconv"
	"strings"
)

// Ref is one issue reference; Owner and Repo are empty for "#n" in the same repository.
type Ref struct {
	Owner, Repo string
	Number      int64
}

// refPart matches "#12" or "owner/name#12"; names follow the forge's name rules loosely, the store decides.
const refPart = `((?:[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+)?#[0-9]+)`

var (
	fenceRE  = regexp.MustCompile("(?s)```.*?(```|$)")
	inlineRE = regexp.MustCompile("`[^`\n]*`")
)

func parseRef(s string) (Ref, bool) {
	path, num, ok := strings.Cut(s, "#")
	if !ok {
		return Ref{}, false
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 1 {
		return Ref{}, false
	}
	r := Ref{Number: n}
	if path != "" {
		r.Owner, r.Repo, _ = strings.Cut(path, "/")
	}
	return r, true
}

// Closing returns the issues a text says it closes: each keyword (case-insensitive, whole word) followed by an
// optional colon and one reference, outside code. Keywords come from the issue.close_keywords setting. Repeats are
// dropped; order is kept.
func Closing(text string, keywords []string) []Ref {
	var words []string
	for _, k := range keywords {
		if k = strings.TrimSpace(k); k != "" {
			words = append(words, regexp.QuoteMeta(k))
		}
	}
	if len(words) == 0 {
		return nil
	}
	re := regexp.MustCompile(`(?i)(?:^|[^\pL\pN_])(?:` + strings.Join(words, "|") + `):?[ \t]+` + refPart + `\b`)
	text = inlineRE.ReplaceAllString(fenceRE.ReplaceAllString(text, " "), " ")
	seen := map[Ref]bool{}
	var out []Ref
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if r, ok := parseRef(m[1]); ok && !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}

// Keywords splits the comma list of the issue.close_keywords setting.
func Keywords(setting string) []string {
	var out []string
	for _, k := range strings.Split(setting, ",") {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			out = append(out, k)
		}
	}
	return out
}

var crossRE = regexp.MustCompile(`(^|[\s(\[])([A-Za-z0-9][A-Za-z0-9_.-]*)/([A-Za-z0-9_.-]+)#([0-9]+)\b`)

// LinkCross turns "owner/name#n" outside fenced code into a Markdown link to that issue, but only when visible says
// the reader may read owner/name; otherwise the text stays plain, so a private repository's name is never made a link
// (D87). Fenced blocks are skipped; inline code is safe because a reference must follow a space or bracket, never a
// backtick.
func LinkCross(md string, visible func(owner, name string) bool) string {
	lines := strings.Split(md, "\n")
	fenced := false
	cache := map[string]bool{}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.Contains(l, "#") {
			continue
		}
		lines[i] = crossRE.ReplaceAllStringFunc(l, func(m string) string {
			sm := crossRE.FindStringSubmatch(m)
			key := sm[2] + "/" + sm[3]
			ok, seen := cache[key]
			if !seen {
				ok = visible(sm[2], sm[3])
				cache[key] = ok
			}
			if !ok {
				return m
			}
			return sm[1] + "[" + key + "#" + sm[4] + "](/" + key + "/isu/" + sm[4] + ")"
		})
	}
	return strings.Join(lines, "\n")
}
