package gitread

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Squash and rebase merges (D88). Like Merge, both work on the bare repository with plumbing only (merge-tree,
// commit-tree, update-ref with the old value): no working tree, no lock files, and the base branch moves only if it
// still points where the caller read it.

// ErrNotLinear: a rebase was asked for a head that contains merge commits; replaying those is not offered.
var ErrNotLinear = errors.New("gitread: the head has merge commits")

// ErrTooMany: a rebase would replay more commits than the caller allows.
var ErrTooMany = errors.New("gitread: too many commits to replay")

// Ident is a commit identity. When is RFC 3339 or git's "<unix> <zone>"; empty = now.
type Ident struct {
	Name, Email, When string
}

func (id Ident) env(role string) []string {
	out := []string{"GIT_" + role + "_NAME=" + id.Name, "GIT_" + role + "_EMAIL=" + id.Email}
	if id.When != "" {
		out = append(out, "GIT_"+role+"_DATE="+id.When)
	}
	return out
}

func (id Ident) orDefault() Ident {
	if id.Name == "" {
		id.Name = "Codr"
	}
	if id.Email == "" {
		id.Email = "codr@localhost"
	}
	return id
}

// commitTree writes one commit object and returns its id.
func (r Repo) commitTree(ctx context.Context, tree string, parents []string, author, committer Ident, message string) (string, error) {
	args := []string{"commit-tree", tree}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	args = append(args, "-F", "-")
	env := append(author.orDefault().env("AUTHOR"), committer.orDefault().env("COMMITTER")...)
	out, msg, code, err := r.execEnv(ctx, env, []byte(message), args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", errors.New("git commit-tree: " + msg)
	}
	id := strings.TrimSpace(string(out))
	if !isHex40(id) {
		return "", errors.New("gitread: commit-tree returned no id")
	}
	return id, nil
}

// moveBranch points refs/heads/<base> at commit only if it still holds expect (ErrStale otherwise).
func (r Repo) moveBranch(ctx context.Context, base, commit, expect, why string) error {
	_, msg, code, err := r.exec(ctx, nil, "update-ref", "-m", why, "refs/heads/"+base, commit, expect)
	switch {
	case err != nil:
		return err
	case code != 0:
		return fmt.Errorf("%w: %s", ErrStale, msg)
	}
	return nil
}

// Squash makes ONE commit on top of base whose tree is the merge of head into base, with author as its author and
// committer as its committer, and moves base to it (compare-and-swap on expectBase).
func (r Repo) Squash(ctx context.Context, base, head, expectBase string, author, committer Ident, message string) (string, error) {
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
	commit, err := r.commitTree(ctx, tree, []string{expectBase}, author, committer, message)
	if err != nil {
		return "", err
	}
	return commit, r.moveBranch(ctx, base, commit, expectBase, "squash: "+head)
}

// Rebase replays each commit of head that base lacks, oldest first, on top of base: every replayed commit keeps its
// author, date and message, and gets committer as its committer. The tree of each step is
// `git merge-tree --merge-base=<parent> <onto> <commit>` (a cherry-pick in memory, git ≥ 2.40). A conflict at any step
// is ErrConflict with that step's paths and nothing moves; merge commits in head are ErrNotLinear; more than max
// commits is ErrTooMany. It returns the new tip.
func (r Repo) Rebase(ctx context.Context, base, head, expectBase string, committer Ident, max int) (string, []string, error) {
	if !ValidRef(base) || !ValidRef(head) || !isHex40(expectBase) {
		return "", nil, ErrBadRef
	}
	headID, err := r.Resolve(ctx, head)
	if err != nil {
		return "", nil, err
	}
	out, err := r.run(ctx, nil, "rev-list", "--reverse", "--topo-order", "--parents", "--end-of-options", expectBase+".."+headID)
	if err != nil {
		return "", nil, err
	}
	var steps [][]string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(l); len(f) > 0 {
			if len(f) != 2 {
				return "", nil, ErrNotLinear // a merge commit (or a root commit that shares nothing with base)
			}
			steps = append(steps, f)
		}
	}
	if len(steps) == 0 {
		return "", nil, ErrStale // nothing to replay: the head is already in base
	}
	if max > 0 && len(steps) > max {
		return "", nil, ErrTooMany
	}
	onto := expectBase
	for _, st := range steps {
		c, parent := st[0], st[1]
		raw, err := r.run(ctx, nil, "show", "-s", "--format=%an%x00%ae%x00%ad%x00%B", "--date=raw", "--end-of-options", c)
		if err != nil {
			return "", nil, err
		}
		f := strings.SplitN(string(raw), "\x00", 4)
		if len(f) != 4 {
			return "", nil, errors.New("gitread: cannot read commit " + c)
		}
		res, msg, code, err := r.exec(ctx, nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", "--merge-base="+parent, "--end-of-options", onto, c)
		if err != nil {
			return "", nil, err
		}
		lines := strings.Split(strings.TrimRight(string(res), "\n"), "\n")
		if len(lines) == 0 || !isHex40(strings.TrimSpace(lines[0])) {
			return "", nil, errors.New("git merge-tree: " + msg)
		}
		if code != 0 {
			var paths []string
			for _, l := range lines[1:] {
				if l = strings.TrimSpace(l); l != "" {
					paths = append(paths, l)
				}
			}
			return "", paths, ErrConflict
		}
		message := strings.TrimRight(f[3], "\n") + "\n"
		if onto, err = r.commitTree(ctx, strings.TrimSpace(lines[0]), []string{onto}, Ident{Name: f[0], Email: f[1], When: f[2]}, committer, message); err != nil {
			return "", nil, err
		}
	}
	return onto, nil, r.moveBranch(ctx, base, onto, expectBase, "rebase: "+head+" ("+strconv.Itoa(len(steps))+" commits)")
}
