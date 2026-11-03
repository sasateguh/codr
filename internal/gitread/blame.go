package gitread

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
)

// BlameLine is one line of a file with the commit that last changed it.
type BlameLine struct {
	N       int    // 1-based line number in the file at ref
	Hash    string // full commit hash
	Author  string
	When    time.Time
	Summary string
	Text    string
	// First marks the first line of a run of consecutive lines from the same commit; the
	// page prints the commit column once per run.
	First bool
}

// Short is the 7-character hash.
func (l BlameLine) Short() string { return short(l.Hash) }

// Blame runs `git blame --porcelain` for one file at ref. Validation is the same as Blob;
// binary or oversize files are the caller's concern (it checks Blob first).
func (r Repo) Blame(ctx context.Context, ref, p string) ([]BlameLine, error) {
	if !ValidRef(ref) {
		return nil, ErrBadRef
	}
	cp, ok := CleanPath(p)
	if !ok || cp == "" {
		return nil, ErrBadPath
	}
	// git blame has no --end-of-options (its revision parser rejects it, measured 14 Oct 2026);
	// ValidRef already refuses a leading dash, and "--" ends the revision list.
	out, err := r.run(ctx, nil, "blame", "--porcelain", ref, "--", cp)
	if err != nil {
		return nil, err
	}
	return parseBlame(out), nil
}

// parseBlame reads porcelain output: a header "<hash> <orig> <final> [<n>]" per line, the
// commit's metadata the first time its hash appears, then a tab-prefixed content line.
func parseBlame(out []byte) []BlameLine {
	type meta struct {
		author, summary string
		when            time.Time
	}
	metas := map[string]*meta{}
	var lines []BlameLine
	var cur BlameLine
	for _, raw := range bytes.Split(out, []byte("\n")) {
		s := string(raw)
		switch {
		case strings.HasPrefix(s, "\t"):
			m := metas[cur.Hash]
			if m != nil {
				cur.Author, cur.Summary, cur.When = m.author, m.summary, m.when
			}
			cur.Text = s[1:]
			cur.First = len(lines) == 0 || lines[len(lines)-1].Hash != cur.Hash
			lines = append(lines, cur)
			cur = BlameLine{}
		case cur.Hash == "" && len(s) >= 40 && isHex40(s[:40]):
			f := strings.Fields(s)
			cur.Hash = f[0]
			if len(f) >= 3 {
				cur.N, _ = strconv.Atoi(f[2])
			}
			if metas[cur.Hash] == nil {
				metas[cur.Hash] = &meta{}
			}
		case cur.Hash != "":
			m := metas[cur.Hash]
			key, val, _ := strings.Cut(s, " ")
			switch key {
			case "author":
				m.author = val
			case "summary":
				m.summary = val
			case "author-time":
				if n, err := strconv.ParseInt(val, 10, 64); err == nil {
					m.when = time.Unix(n, 0).UTC()
				}
			}
		}
	}
	return lines
}

func isHex40(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
