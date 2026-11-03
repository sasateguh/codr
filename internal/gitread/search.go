package gitread

import (
	"bytes"
	"context"
	"strconv"
	"strings"
)

// GrepHit is one matching line.
type GrepHit struct {
	Path string
	N    int
	Text string
}

// Files lists every file path at ref (git ls-tree -r). Callers filter; git does no matching.
func (r Repo) Files(ctx context.Context, ref string) ([]string, error) {
	if !ValidRef(ref) {
		return nil, ErrBadRef
	}
	out, err := r.run(ctx, nil, "ls-tree", "-r", "--name-only", "-z", "--end-of-options", ref)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			files = append(files, string(p))
		}
	}
	return files, nil
}

// Grep searches file contents at ref: fixed string, case-insensitive, text files only, at
// most perFile hits per file and max hits in all (git's -m plus a cut here). The query is
// passed after -e so a leading dash is data, never an option. capped reports the cut.
func (r Repo) Grep(ctx context.Context, ref, query string, perFile, max int) (hits []GrepHit, capped bool, err error) {
	if !ValidRef(ref) {
		return nil, false, ErrBadRef
	}
	if query == "" || strings.ContainsRune(query, 0) {
		return nil, false, nil
	}
	out, err := r.run(ctx, nil, "grep", "-n", "-I", "-i", "-z", "--fixed-strings", "-m", strconv.Itoa(perFile), "-e", query, ref, "--")
	if err != nil {
		if strings.Contains(err.Error(), "exit status 1") { // no match
			return nil, false, nil
		}
		return nil, false, err
	}
	// -z: "<ref>:<path>\0<line>\0<text>\n"
	prefix := []byte(ref + ":")
	for _, rec := range bytes.Split(out, []byte("\n")) {
		if len(rec) == 0 {
			continue
		}
		f := bytes.SplitN(rec, []byte{0}, 3)
		if len(f) != 3 || !bytes.HasPrefix(f[0], prefix) {
			continue
		}
		n, _ := strconv.Atoi(string(f[1]))
		if len(hits) >= max {
			return hits, true, nil
		}
		hits = append(hits, GrepHit{Path: string(f[0][len(prefix):]), N: n, Text: string(f[2])})
	}
	return hits, false, nil
}
