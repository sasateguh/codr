package repos

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateDelete(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	d := Disk{Root: filepath.Join(t.TempDir(), "repos"), GitBinary: "git"}
	ctx := context.Background()
	if err := d.Create(ctx, 7, "main"); err != nil {
		t.Fatal(err)
	}
	if !d.Exists(7) {
		t.Fatal("repo should exist")
	}
	head, _ := os.ReadFile(filepath.Join(d.Path(7), "HEAD"))
	if string(head) != "ref: refs/heads/main\n" {
		t.Fatalf("HEAD = %q", head)
	}
	if err := d.Create(ctx, 7, "main"); err == nil {
		t.Fatal("duplicate create must fail")
	}
	if err := d.Create(ctx, 8, "--upload-pack=x"); err == nil {
		t.Fatal("branch starting with - must be rejected")
	}
	if err := d.SetDefaultBranch(ctx, 7, "trunk"); err != nil {
		t.Fatal(err)
	}
	head, _ = os.ReadFile(filepath.Join(d.Path(7), "HEAD"))
	if string(head) != "ref: refs/heads/trunk\n" {
		t.Fatalf("HEAD = %q", head)
	}
	if err := d.Delete(7); err != nil {
		t.Fatal(err)
	}
	if d.Exists(7) {
		t.Fatal("repo should be gone")
	}
	if err := d.Delete(7); err == nil {
		t.Fatal("deleting a missing repo must fail, not silently succeed")
	}
	if err := d.Create(ctx, 0, "main"); err == nil {
		t.Fatal("id 0 must be rejected")
	}
}

// TestForkAndFetchBranch: a fork carries every branch and tag and no remote; a branch fetched back lands only
// in its refs/codr/ ref; bad names are refused before git runs (D75).
func TestForkAndFetchBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	d := Disk{Root: filepath.Join(t.TempDir(), "repos"), GitBinary: "git"}
	ctx := context.Background()
	if err := d.Create(ctx, 1, "main"); err != nil {
		t.Fatal(err)
	}
	w := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-C", dir}, args...)...)
		cmd.Env = minimalEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git(w, "init", "-q")
	_ = os.WriteFile(filepath.Join(w, "a"), []byte("1"), 0o600)
	git(w, "add", ".")
	git(w, "commit", "-q", "-m", "satu")
	git(w, "tag", "v1")
	git(w, "branch", "ciri")
	git(w, "push", "-q", d.Path(1), "main", "ciri", "v1")
	if err := d.Fork(ctx, 1, 2); err != nil {
		t.Fatal(err)
	}
	refs := git(d.Path(2), "for-each-ref", "--format=%(refname)")
	for _, want := range []string{"refs/heads/main", "refs/heads/ciri", "refs/tags/v1"} {
		if !strings.Contains(refs, want) {
			t.Fatalf("fork lacks %s: %s", want, refs)
		}
	}
	if remotes := git(d.Path(2), "remote"); strings.TrimSpace(remotes) != "" {
		t.Fatalf("fork kept a remote: %q", remotes)
	}
	if err := d.Fork(ctx, 1, 2); err == nil {
		t.Fatal("fork over an existing repo")
	}
	// a new commit on the fork's branch, fetched into the source under refs/codr/
	w2 := t.TempDir()
	git(w2, "clone", "-q", d.Path(2), ".")
	git(w2, "checkout", "-q", "ciri")
	_ = os.WriteFile(filepath.Join(w2, "b"), []byte("2"), 0o600)
	git(w2, "add", ".")
	git(w2, "commit", "-q", "-m", "dari fork")
	git(w2, "push", "-q", "origin", "ciri")
	if err := d.FetchBranch(ctx, 2, "ciri", 1, "refs/codr/pull/9"); err != nil {
		t.Fatal(err)
	}
	if got := git(d.Path(1), "log", "-1", "--format=%s", "refs/codr/pull/9"); strings.TrimSpace(got) != "dari fork" {
		t.Fatalf("fetched ref: %q", got)
	}
	if got := git(d.Path(1), "log", "-1", "--format=%s", "refs/heads/ciri"); strings.TrimSpace(got) != "satu" {
		t.Fatal("the source's own branch moved")
	}
	for _, bad := range [][2]string{{"--upload-pack=x", "refs/codr/x"}, {"ciri", "refs/heads/main"}} {
		if err := d.FetchBranch(ctx, 2, bad[0], 1, bad[1]); err == nil {
			t.Fatalf("FetchBranch(%q, %q) accepted", bad[0], bad[1])
		}
	}
}
