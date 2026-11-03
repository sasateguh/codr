package gitread

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Pull request plumbing (FR-22, D53). These are the only write operations in this package:
// a merge creates one commit object and moves one branch ref with a compare-and-swap, so a
// push that lands in between is never overwritten. Everything is `exec git`, never a reimplementation.

// ErrConflict is returned by Merge when the two branches cannot be merged cleanly.
var ErrConflict = errors.New("gitread: merge conflict")

// ErrStale is returned by Merge when the base branch moved since it was read.
var ErrStale = errors.New("gitread: base branch moved")

// MergeBase returns the best common ancestor of two refs.
func (r Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	if !ValidRef(a) || !ValidRef(b) {
		return "", ErrBadRef
	}
	out, err := r.run(ctx, nil, "merge-base", "--end-of-options", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsAncestor reports whether a is reachable from b (b contains a).
func (r Repo) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	if !ValidRef(a) || !ValidRef(b) {
		return false, ErrBadRef
	}
	_, msg, code, err := r.exec(ctx, nil, "merge-base", "--is-ancestor", "--end-of-options", a, b)
	switch {
	case err != nil:
		return false, err
	case code == 0:
		return true, nil
	case code == 1:
		return false, nil
	}
	return false, errors.New("git merge-base: " + msg)
}

// LogRange lists the commits in head that are not in base (newest first), at most n.
func (r Repo) LogRange(ctx context.Context, base, head string, n int) ([]Commit, error) {
	if !ValidRef(base) || !ValidRef(head) {
		return nil, ErrBadRef
	}
	out, err := r.run(ctx, nil, "log", "--max-count="+strconv.Itoa(n), "--format="+logFormat, "--end-of-options", base+".."+head)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

// DiffRange computes what head would add to base: the three-dot diff from the merge base.
func (r Repo) DiffRange(ctx context.Context, base, head string, maxLines int) (Diff, error) {
	if !ValidRef(base) || !ValidRef(head) {
		return Diff{}, ErrBadRef
	}
	mb, err := r.MergeBase(ctx, base, head)
	if err != nil {
		return Diff{}, err
	}
	var d Diff
	stat, err := r.run(ctx, nil, "diff-tree", "-r", "-M", "--numstat", "--end-of-options", mb, head)
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
	patch, err := r.run(ctx, nil, "diff-tree", "-r", "-M", "-p", "--no-color", "--end-of-options", mb, head)
	if err != nil {
		return d, err
	}
	d.Files, d.Truncated = parsePatch(patch, maxLines)
	return d, nil
}

// MergeTree tries the merge in memory: the resulting tree id on success, ErrConflict with
// the conflicting paths otherwise (git merge-tree --write-tree, git ≥ 2.38).
func (r Repo) MergeTree(ctx context.Context, base, head string) (tree string, conflicts []string, err error) {
	if !ValidRef(base) || !ValidRef(head) {
		return "", nil, ErrBadRef
	}
	out, msg, code, err := r.exec(ctx, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--end-of-options", base, head)
	if err != nil {
		return "", nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 || !isHex40(strings.TrimSpace(lines[0])) {
		return "", nil, errors.New("git merge-tree: " + msg)
	}
	tree = strings.TrimSpace(lines[0])
	if code == 0 {
		return tree, nil, nil
	}
	for _, l := range lines[1:] {
		if l = strings.TrimSpace(l); l != "" {
			conflicts = append(conflicts, l)
		}
	}
	return tree, conflicts, ErrConflict
}

// Merge creates a merge commit of head into base and moves refs/heads/<base> to it, only if the
// branch still points at expectBase (compare-and-swap). The author is the person merging.
func (r Repo) Merge(ctx context.Context, base, head, expectBase, authorName, authorEmail, message string) (string, error) {
	if !ValidRef(base) || !ValidRef(head) || !isHex40(expectBase) {
		return "", ErrBadRef
	}
	headID, err := r.Resolve(ctx, head)
	if err != nil {
		return "", err
	}
	tree, _, err := r.MergeTree(ctx, expectBase, headID)
	if err != nil {
		return "", err
	}
	if authorName == "" {
		authorName = "Codr"
	}
	if authorEmail == "" {
		authorEmail = "codr@localhost"
	}
	ident := []string{"-c", "user.name=" + authorName, "-c", "user.email=" + authorEmail}
	out, err := r.run(ctx, []byte(message), append(ident, "commit-tree", tree, "-p", expectBase, "-p", headID, "-F", "-")...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(out))
	if !isHex40(commit) {
		return "", errors.New("gitread: commit-tree returned no id")
	}
	_, msg, code, err := r.exec(ctx, nil, "update-ref", "-m", "merge: "+head, "refs/heads/"+base, commit, expectBase)
	switch {
	case err != nil:
		return "", err
	case code != 0:
		return "", fmt.Errorf("%w: %s", ErrStale, msg)
	}
	return commit, nil
}
