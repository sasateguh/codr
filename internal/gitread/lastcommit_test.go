package gitread

import (
	"context"
	"testing"
)

// TestLastCommits: each entry gets the newest commit that touched it, directories by any file inside,
// and a window too small to reach an entry leaves it out instead of guessing.
func TestLastCommits(t *testing.T) {
	r := fixture(t)
	ctx := context.Background()
	got, err := r.LastCommits(ctx, "main", "", []string{"README.md", "src", "logo.bin"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got["README.md"].Subject != "pertama" || got["src"].Subject != "kedua: nama semula + binari" || got["logo.bin"].Subject != "kedua: nama semula + binari" {
		t.Fatalf("root: %+v", got)
	}
	if got["src"].Hash == "" || got["src"].When.IsZero() {
		t.Fatalf("commit fields empty: %+v", got["src"])
	}
	sub, err := r.LastCommits(ctx, "main", "src", []string{"app.go"}, 100)
	if err != nil || sub["app.go"].Subject != "kedua: nama semula + binari" {
		t.Fatalf("subdir: %+v %v", sub, err)
	}
	if one, _ := r.LastCommits(ctx, "main", "", []string{"README.md", "src"}, 1); len(one) != 1 {
		t.Fatalf("window of one commit must find only what it touched: %+v", one)
	}
	if _, err := r.LastCommits(ctx, "--bad", "", []string{"x"}, 10); err == nil {
		t.Fatal("bad ref accepted")
	}
	if _, err := r.LastCommits(ctx, "main", "../x", []string{"x"}, 10); err == nil {
		t.Fatal("bad path accepted")
	}
}

// TestTreeSizes: every blob with its size, symlinks and trees left out, the cap honoured.
func TestTreeSizes(t *testing.T) {
	r := fixture(t)
	files, capped, err := r.TreeSizes(context.Background(), "main", 100)
	if err != nil || capped || len(files) != 3 {
		t.Fatalf("%+v %v %v", files, capped, err)
	}
	seen := map[string]int64{}
	for _, f := range files {
		seen[f.Path] = f.Size
	}
	if seen["src/app.go"] == 0 || seen["logo.bin"] != 3 {
		t.Fatalf("sizes: %+v", seen)
	}
	if one, capped, _ := r.TreeSizes(context.Background(), "main", 1); len(one) != 1 || !capped {
		t.Fatal("cap not honoured")
	}
}
