package gitread

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"
)

// LastCommits finds, for each name directly under dir at ref, the newest commit that touched it (D61).
// It is ONE `git log --name-only` walk limited to maxCommits, not one process per entry; names whose
// last change is older than that window are simply absent from the result.
func (r Repo) LastCommits(ctx context.Context, ref, dir string, names []string, maxCommits int) (map[string]Commit, error) {
	if !ValidRef(ref) {
		return nil, ErrBadRef
	}
	cp, ok := CleanPath(dir)
	if !ok {
		return nil, ErrBadPath
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	found := make(map[string]Commit, len(names))
	if len(want) == 0 || maxCommits <= 0 {
		return found, nil
	}
	args := []string{"-c", "core.quotePath=false", "log", "--format=%x1e%H%x00%at%x00%an%x00%s", "--name-only", "--no-renames",
		"-n", strconv.Itoa(maxCommits), "--end-of-options", ref}
	if cp != "" {
		args = append(args, "--", cp)
	}
	out, err := r.run(ctx, nil, args...)
	if err != nil {
		return nil, err
	}
	prefix := ""
	if cp != "" {
		prefix = cp + "/"
	}
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		head, files, _ := strings.Cut(string(rec), "\n")
		f := strings.SplitN(head, "\x00", 4)
		if len(f) != 4 {
			continue
		}
		var c *Commit
		for _, path := range strings.Split(files, "\n") {
			rest, ok := strings.CutPrefix(path, prefix)
			if !ok || rest == "" {
				continue
			}
			name, _, _ := strings.Cut(rest, "/")
			if !want[name] {
				continue
			}
			if _, done := found[name]; done {
				continue
			}
			if c == nil {
				ts, _ := strconv.ParseInt(f[1], 10, 64)
				c = &Commit{Hash: f[0], When: time.Unix(ts, 0).UTC(), Author: f[2], Subject: f[3]}
			}
			found[name] = *c
		}
		if len(found) == len(want) {
			break
		}
	}
	return found, nil
}
