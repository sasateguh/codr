package gitread

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	work, bare := filepath.Join(dir, "w"), filepath.Join(dir, "r.git")
	tick := 0 // each commit gets a later timestamp: creatordate ordering must never tie
	run := func(args ...string) string {
		t.Helper()
		if args[0] == "commit" {
			tick++
		}
		stamp := time.Date(2026, 10, 14, 1, 2, tick, 0, time.UTC).Format(time.RFC3339)
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-C", work}, args...)...)
		cmd.Env = append(cleanEnv(), "HOME="+dir, "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	_ = os.MkdirAll(filepath.Join(work, "src"), 0o700)
	_ = os.WriteFile(filepath.Join(work, "README.md"), []byte("# Projek\n\nHalo *dunia*.\n"), 0o600)
	_ = os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600)
	_ = os.WriteFile(filepath.Join(work, "logo.bin"), []byte{0, 1, 2, 3, 0xff}, 0o600)
	run("init", "-q")
	run("add", ".")
	run("commit", "-q", "-m", "pertama\n\nbadan mesej")
	run("tag", "v0.1")
	run("mv", "src/main.go", "src/app.go")
	_ = os.WriteFile(filepath.Join(work, "src", "app.go"), []byte("package main\n\nfunc main() {}\n\n// nota: fmt.Println(1)\n"), 0o600)
	_ = os.WriteFile(filepath.Join(work, "logo.bin"), []byte{0, 9, 9}, 0o600)
	run("add", "-A")
	run("commit", "-q", "-m", "kedua: nama semula + binari")
	run("checkout", "-q", "-b", "kerja")
	_ = os.WriteFile(filepath.Join(work, "nota.txt"), []byte("x\n"), 0o600)
	run("add", "-A")
	run("commit", "-q", "-m", "ketiga di kerja")
	run("checkout", "-q", "main")
	run("clone", "-q", "--bare", work, bare)
	return Repo{Path: bare, Git: "git", Timeout: 10 * time.Second}
}

func TestValidation(t *testing.T) {
	for _, bad := range []string{"", "-x", "--output=/tmp/x", "a..b", "/main", "main/", "x.lock", "ma in", "a\nb"} {
		if ValidRef(bad) {
			t.Errorf("ValidRef(%q) = true", bad)
		}
	}
	for _, ok := range []string{"main", "release/1.0", "v0.1", "abc1234", "feature-x_y.z", "HEAD"} {
		if !ValidRef(ok) {
			t.Errorf("ValidRef(%q) = false", ok)
		}
	}
	for _, bad := range []string{"/etc", "../x", "a/../b", "a//b", "./a", "a/./b"} {
		if _, ok := CleanPath(bad); ok {
			t.Errorf("CleanPath(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"", "src", "src/app.go", "-weird-name.txt"} {
		if _, good := CleanPath(ok); !good {
			t.Errorf("CleanPath(%q) refused", ok)
		}
	}
}

func TestReadOps(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	branch, has, err := r.Head(ctx)
	if err != nil || branch != "main" || !has {
		t.Fatalf("Head: %s %v %v", branch, has, err)
	}
	bs, _ := r.Branches(ctx)
	if len(bs) != 2 || bs[0].Name != "kerja" { // newest commit first
		t.Fatalf("Branches: %+v", bs)
	}
	tags, _ := r.Tags(ctx)
	if len(tags) != 1 || tags[0].Name != "v0.1" {
		t.Fatalf("Tags: %+v", tags)
	}
	if _, err := r.Resolve(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("Resolve nope: %v", err)
	}
	if _, err := r.Resolve(ctx, "--all"); err != ErrBadRef {
		t.Fatal("option accepted as ref")
	}
	log, err := r.Log(ctx, "main", "", 10, 0)
	if err != nil || len(log) != 2 || log[0].Subject != "kedua: nama semula + binari" || log[1].Body != "badan mesej" {
		t.Fatalf("Log: %v %+v", err, log)
	}
	if log[0].When.Year() != 2026 || log[0].Author != "t" || len(log[0].Parents) != 1 {
		t.Fatalf("commit meta: %+v", log[0])
	}
	if n, _ := r.Count(ctx, "kerja"); n != 3 {
		t.Fatalf("Count = %d", n)
	}
	onlySrc, _ := r.Log(ctx, "main", "src", 10, 0)
	if len(onlySrc) != 2 {
		t.Fatalf("path log = %d", len(onlySrc))
	}
	tree, err := r.Tree(ctx, "main", "")
	if err != nil || len(tree) != 3 || !tree[0].IsDir() || tree[0].Name != "src" || tree[1].Name != "README.md" || tree[1].Size != 24 {
		t.Fatalf("Tree: %v %+v", err, tree)
	}
	if _, err := r.Tree(ctx, "main", "nope"); err != ErrNotFound {
		t.Fatalf("Tree missing path: %v", err)
	}
	if _, err := r.Tree(ctx, "main", "../x"); err != ErrBadPath {
		t.Fatal("path escape accepted")
	}
	b, err := r.Blob(ctx, "main", "src/app.go", 1<<20)
	if err != nil || b.Binary || !strings.Contains(string(b.Content), "fmt.Println") || b.Size != int64(len(b.Content)) {
		t.Fatalf("Blob: %v %+v", err, b)
	}
	bin, _ := r.Blob(ctx, "main", "logo.bin", 1<<20)
	if !bin.Binary {
		t.Fatal("binary not detected")
	}
	small, _ := r.Blob(ctx, "main", "README.md", 5)
	if !small.Truncated || len(small.Content) != 5 || small.Size != 24 {
		t.Fatalf("truncate: %+v", small)
	}
	if _, err := r.Blob(ctx, "main", "src", 1<<20); err != ErrNotFound {
		t.Fatalf("Blob on dir: %v", err)
	}
	if name, ok := r.README(ctx, "main"); !ok || name != "README.md" {
		t.Fatal("README not found")
	}
	d, err := r.Diff(ctx, log[0].Hash, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stats) != 2 || len(d.Files) != 2 {
		t.Fatalf("diff stats=%d files=%d: %+v", len(d.Stats), len(d.Files), d.Stats)
	}
	var sawRename, sawBinary bool
	for _, f := range d.Files {
		if f.OldPath == "src/main.go" && f.Path == "src/app.go" {
			sawRename = true
			var adds int
			for _, l := range f.Lines {
				if l.Kind == "add" {
					adds++
					if l.New == 0 {
						t.Fatal("add line without new number")
					}
				}
			}
			if adds != 2 { // blank line + comment
				t.Fatalf("rename adds = %d", adds)
			}
		}
		if f.Path == "logo.bin" && f.Binary {
			sawBinary = true
		}
	}
	if !sawRename || !sawBinary {
		t.Fatalf("rename=%v binary=%v: %+v", sawRename, sawBinary, d.Files)
	}
	for _, s := range d.Stats {
		if s.Path == "src/app.go" && s.OldPath != "src/main.go" {
			t.Fatalf("numstat rename not split: %+v", s)
		}
	}
	root, _ := r.Diff(ctx, log[1].Hash, 2)
	if !root.Truncated || len(root.Stats) != 3 {
		t.Fatalf("root diff truncation: %+v", root)
	}
}

func TestEmptyRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", "--bare", "--initial-branch=main", dir).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	r := Repo{Path: dir, Git: "git", Timeout: 5 * time.Second}
	branch, has, err := r.Head(context.Background())
	if err != nil || branch != "main" || has {
		t.Fatalf("empty Head: %s %v %v", branch, has, err)
	}
	if _, err := r.Tree(context.Background(), "main", ""); err != ErrNotFound {
		t.Fatalf("empty Tree: %v", err)
	}
}

// cleanEnv drops inherited GIT_* variables: under a git hook GIT_DIR/GIT_INDEX_FILE point at the
// caller's repo and every fixture command would land there instead of the temp dir.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			out = append(out, kv)
		}
	}
	return out
}

func TestBlame(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	lines, err := r.Blame(ctx, "main", "src/app.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("lines: %d", len(lines))
	}
	first, err := r.Resolve(ctx, "v0.1")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := r.Resolve(ctx, "main")
	// lines 1–3 came with the first commit (rename keeps history), 4–5 with the second
	for i, l := range lines {
		want := first
		if i >= 3 {
			want = head
		}
		if l.Hash != want || l.N != i+1 || l.Author != "t" || l.Summary == "" || l.When.IsZero() {
			t.Fatalf("line %d: %+v (want %s)", i+1, l, short(want))
		}
	}
	if !lines[0].First || lines[1].First || !lines[3].First || lines[4].First || lines[4].Text != "// nota: fmt.Println(1)" {
		t.Fatalf("runs: %+v", lines)
	}
	if _, err := r.Blame(ctx, "main", "../x"); err != ErrBadPath {
		t.Fatal("bad path must be refused")
	}
	if _, err := r.Blame(ctx, "main", "tiada.txt"); err == nil {
		t.Fatal("missing file must fail")
	}
}

func TestFilesAndGrep(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	files, err := r.Files(ctx, "main")
	if err != nil || len(files) != 3 || files[0] != "README.md" || files[2] != "src/app.go" {
		t.Fatalf("files: %v %v", files, err)
	}
	hits, capped, err := r.Grep(ctx, "main", "MAIN", 5, 100) // case-insensitive, fixed string
	if err != nil || capped || len(hits) != 2 || hits[0].Path != "src/app.go" || hits[0].N != 1 || !strings.Contains(hits[1].Text, "func main") {
		t.Fatalf("grep: %+v %v %v", hits, capped, err)
	}
	if hits, capped, _ := r.Grep(ctx, "main", "main", 5, 1); len(hits) != 1 || !capped {
		t.Fatalf("cap: %+v %v", hits, capped)
	}
	if hits, _, err := r.Grep(ctx, "main", "tiada-di-mana-mana", 5, 10); err != nil || len(hits) != 0 {
		t.Fatalf("no match: %+v %v", hits, err)
	}
	if hits, _, err := r.Grep(ctx, "main", "-n", 5, 10); err != nil || len(hits) != 0 { // a dash is data
		t.Fatalf("dash query: %+v %v", hits, err)
	}
	if hits, _, err := r.Grep(ctx, "main", "func main() {}", 5, 10); err != nil || len(hits) != 1 { // regex chars are literal
		t.Fatalf("literal query: %+v %v", hits, err)
	}
	if _, err := r.Files(ctx, "-x"); err != ErrBadRef {
		t.Fatal("bad ref")
	}
}
