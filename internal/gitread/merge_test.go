package gitread

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mergeFixture builds a bare repo with main (2 commits), a clean feature branch and a
// conflicting branch, and returns the repo plus a git runner on the working copy.
func mergeFixture(t *testing.T) (Repo, func(args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	work, bare := filepath.Join(dir, "w"), filepath.Join(dir, "r.git")
	tick := 0
	run := func(args ...string) string {
		t.Helper()
		if args[0] == "commit" {
			tick++
		}
		stamp := time.Date(2026, 10, 15, 1, 2, tick, 0, time.UTC).Format(time.RFC3339)
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-C", work}, args...)...)
		cmd.Env = append(cleanEnv(), "HOME="+dir, "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) { _ = os.WriteFile(filepath.Join(work, name), []byte(body), 0o600) }
	_ = os.MkdirAll(work, 0o700)
	run("init", "-q")
	write("a.txt", "satu\n")
	run("add", ".")
	run("commit", "-q", "-m", "a: satu")
	write("b.txt", "b\n")
	run("add", ".")
	run("commit", "-q", "-m", "b: tambah")
	// clean feature: new file + an unrelated change
	run("checkout", "-q", "-b", "ciri")
	write("c.txt", "c\n")
	run("add", ".")
	run("commit", "-q", "-m", "ciri: fail c")
	write("c.txt", "c\ncc\n")
	run("add", ".")
	run("commit", "-q", "-m", "ciri: baris kedua")
	// conflicting branch: edits a.txt differently from main
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "konflik")
	write("a.txt", "dua\n")
	run("add", ".")
	run("commit", "-q", "-m", "konflik: a dua")
	run("checkout", "-q", "main")
	write("a.txt", "tiga\n")
	run("add", ".")
	run("commit", "-q", "-m", "main: a tiga")
	run("init", "-q", "--bare", bare)
	run("push", "-q", bare, "main", "ciri", "konflik")
	return Repo{Path: bare, Git: "git", Timeout: 10 * time.Second}, run
}

func TestMergeRangeAndTree(t *testing.T) {
	r, _ := mergeFixture(t)
	ctx := context.Background()
	mainID, _ := r.Resolve(ctx, "main")
	cs, err := r.LogRange(ctx, "main", "ciri", 50)
	if err != nil || len(cs) != 2 || cs[0].Subject != "ciri: baris kedua" {
		t.Fatalf("log range: %v %v", cs, err)
	}
	d, err := r.DiffRange(ctx, "main", "ciri", 500)
	if err != nil || len(d.Stats) != 1 || d.Stats[0].Path != "c.txt" || d.Added != 2 {
		t.Fatalf("diff range (three-dot: main's own change to a.txt must not appear): %+v %v", d, err)
	}
	if ok, _ := r.IsAncestor(ctx, "ciri", "main"); ok {
		t.Fatal("ciri is not an ancestor of main")
	}
	if tree, conflicts, err := r.MergeTree(ctx, "main", "ciri"); err != nil || tree == "" || len(conflicts) != 0 {
		t.Fatalf("clean merge-tree: %s %v %v", tree, conflicts, err)
	}
	if _, conflicts, err := r.MergeTree(ctx, "main", "konflik"); !errors.Is(err, ErrConflict) || len(conflicts) != 1 || conflicts[0] != "a.txt" {
		t.Fatalf("conflict detection: %v %v", conflicts, err)
	}
	// merge with a stale expectation is refused (compare-and-swap)
	if _, err := r.Merge(ctx, "main", "ciri", strings.Repeat("0", 40), "Aiman", "a@x", "m"); err == nil {
		t.Fatal("stale base must be refused")
	}
	commit, err := r.Merge(ctx, "main", "ciri", mainID, "Aiman", "aiman@example.invalid", "Merge ciri into main\n\nPR #3")
	if err != nil || !isHex40(commit) {
		t.Fatalf("merge: %s %v", commit, err)
	}
	c, _ := r.Commit(ctx, "main")
	if c.Hash != commit || len(c.Parents) != 2 || c.Parents[0] != mainID || c.Author != "Aiman" || c.Subject != "Merge ciri into main" {
		t.Fatalf("merge commit: %+v", c)
	}
	if ok, _ := r.IsAncestor(ctx, "ciri", "main"); !ok {
		t.Fatal("after merge, ciri must be contained in main")
	}
	entries, _ := r.Tree(ctx, "main", "")
	names := ""
	for _, e := range entries {
		names += e.Name + " "
	}
	if !strings.Contains(names, "c.txt") || !strings.Contains(names, "a.txt") {
		t.Fatalf("merged tree: %s", names)
	}
	if _, err := r.Merge(ctx, "main", "konflik", commit, "Aiman", "a@x", "m"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict merge must fail: %v", err)
	}
	if _, err := r.Merge(ctx, "main", "--upload-pack=x", commit, "a", "a", "m"); !errors.Is(err, ErrBadRef) {
		t.Fatalf("bad ref: %v", err)
	}
}
