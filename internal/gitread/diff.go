package gitread

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"strings"
)

// FileStat is one line of --numstat with the rename/binary flags.
type FileStat struct {
	Path    string
	OldPath string // set when renamed
	Added   int
	Deleted int
	Binary  bool
}

// Line is one rendered diff line.
type Line struct {
	Kind string // ctx | add | del | hunk
	Old  int    // 0 = none
	New  int
	Text string
}

// FileDiff is the patch of one file.
type FileDiff struct {
	Path    string
	OldPath string
	Binary  bool
	Lines   []Line
}

// Diff is a whole commit's change set.
type Diff struct {
	Stats     []FileStat
	Files     []FileDiff
	Added     int
	Deleted   int
	Truncated bool // patch cut at maxLines
}

// Diff computes the change set of a commit against its first parent (root commits against
// the empty tree). The patch stops after maxLines lines; stats are always complete.
func (r Repo) Diff(ctx context.Context, hash string, maxLines int) (Diff, error) {
	if !ValidRef(hash) {
		return Diff{}, ErrBadRef
	}
	var d Diff
	stat, err := r.run(ctx, nil, "diff-tree", "--root", "--no-commit-id", "-r", "-M", "--numstat", "--end-of-options", hash)
	if err != nil {
		return d, err
	}
	for _, line := range strings.Split(strings.TrimRight(string(stat), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		s := FileStat{Path: f[2]}
		if f[0] == "-" {
			s.Binary = true
		} else {
			s.Added, _ = strconv.Atoi(f[0])
			s.Deleted, _ = strconv.Atoi(f[1])
		}
		s.OldPath, s.Path = splitRename(s.Path)
		d.Added += s.Added
		d.Deleted += s.Deleted
		d.Stats = append(d.Stats, s)
	}
	patch, err := r.run(ctx, nil, "diff-tree", "--root", "--no-commit-id", "-r", "-M", "-p", "--no-color", "--end-of-options", hash)
	if err != nil {
		return d, err
	}
	d.Files, d.Truncated = parsePatch(patch, maxLines)
	return d, nil
}

// splitRename turns git's "dir/{old => new}.go" / "old => new" into (old, new).
func splitRename(p string) (oldPath, newPath string) {
	if !strings.Contains(p, " => ") {
		return "", p
	}
	if i := strings.Index(p, "{"); i >= 0 {
		if j := strings.Index(p[i:], "}"); j > 0 {
			pre, mid, post := p[:i], p[i+1:i+j], p[i+j+1:]
			o, n, _ := strings.Cut(mid, " => ")
			return cleanJoin(pre, o, post), cleanJoin(pre, n, post)
		}
	}
	o, n, _ := strings.Cut(p, " => ")
	return o, n
}

func cleanJoin(pre, mid, post string) string {
	s := pre + mid + post
	return strings.ReplaceAll(s, "//", "/")
}

// parsePatch walks a unified diff and numbers the lines. It never fails: malformed input
// just yields fewer lines.
func parsePatch(patch []byte, maxLines int) ([]FileDiff, bool) {
	var files []FileDiff
	var cur *FileDiff
	var oldN, newN, total int
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files = append(files, FileDiff{})
			cur = &files[len(files)-1]
			a, b := parseDiffHeader(line)
			cur.Path = b
			if a != b {
				cur.OldPath = a
			}
			oldN, newN = 0, 0
		case cur == nil:
			continue
		case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "rename from "):
			cur.OldPath = strings.TrimPrefix(line, "rename from ")
		case strings.HasPrefix(line, "rename to "):
			cur.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "new file mode"), strings.HasPrefix(line, "deleted file mode"), strings.HasPrefix(line, "similarity index"),
			strings.HasPrefix(line, "old mode"), strings.HasPrefix(line, "new mode"):
			continue
		case strings.HasPrefix(line, "@@"):
			oldN, newN = hunkStart(line)
			cur.Lines = append(cur.Lines, Line{Kind: "hunk", Text: line})
		case strings.HasPrefix(line, "+"):
			cur.Lines = append(cur.Lines, Line{Kind: "add", New: newN, Text: line[1:]})
			newN++
		case strings.HasPrefix(line, "-"):
			cur.Lines = append(cur.Lines, Line{Kind: "del", Old: oldN, Text: line[1:]})
			oldN++
		case strings.HasPrefix(line, "\\"):
			cur.Lines = append(cur.Lines, Line{Kind: "ctx", Text: line})
		default:
			cur.Lines = append(cur.Lines, Line{Kind: "ctx", Old: oldN, New: newN, Text: strings.TrimPrefix(line, " ")})
			oldN++
			newN++
		}
		if cur != nil && len(cur.Lines) > 0 && cur.Lines[len(cur.Lines)-1].Kind != "hunk" {
			total++
			if maxLines > 0 && total >= maxLines {
				return files, true
			}
		}
	}
	return files, false
}

func parseDiffHeader(line string) (a, b string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	// forms: a/x b/x  (quoted when unusual chars — kept as-is)
	if strings.HasPrefix(rest, "a/") {
		if i := strings.Index(rest, " b/"); i > 0 {
			return rest[2:i], rest[i+3:]
		}
	}
	f := strings.Fields(rest)
	if len(f) >= 2 {
		return strings.TrimPrefix(f[0], "a/"), strings.TrimPrefix(f[1], "b/")
	}
	return rest, rest
}

func hunkStart(h string) (oldN, newN int) {
	// @@ -a,b +c,d @@ ...
	f := strings.Fields(h)
	if len(f) < 3 {
		return 1, 1
	}
	oldN = atoiPrefix(strings.TrimPrefix(f[1], "-"))
	newN = atoiPrefix(strings.TrimPrefix(f[2], "+"))
	return oldN, newN
}

func atoiPrefix(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n, _ := strconv.Atoi(s)
	return n
}
