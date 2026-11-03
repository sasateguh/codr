package wiki

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWiki(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	w := Wiki{Path: filepath.Join(t.TempDir(), "1.wiki.git"), Git: "git", Timeout: 30 * time.Second}
	alice := Author{"alice", "alice@contoh.invalid"}
	if ps, err := w.Pages(ctx); err != nil || len(ps) != 0 {
		t.Fatal("empty wiki")
	}
	c1, err := w.Save(ctx, Home, "", []byte("# Selamat datang\n"), alice, "Cipta Home", "")
	if err != nil || len(c1) != 40 {
		t.Fatalf("first save: %v", err)
	}
	if _, err := w.Save(ctx, Home, "", []byte("lagi"), alice, "x", ""); !errors.Is(err, ErrConflict) {
		t.Fatal("a new page over an existing name")
	}
	c2, err := w.Save(ctx, "Panduan-Pasang", "", []byte("Langkah 1"), alice, "Panduan", c1)
	if err != nil {
		t.Fatal(err)
	}
	// two editors open Home at c2; the first saves; the second's save of the SAME page is a conflict, not a silent loss
	if _, err := w.Save(ctx, Home, "", []byte("versi A"), alice, "A", c2); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Save(ctx, Home, "", []byte("versi B"), alice, "B", c2); !errors.Is(err, ErrConflict) {
		t.Fatalf("second editor overwrote the first: %v", err)
	}
	// ...but editing ANOTHER page from the old base goes through
	if _, err := w.Save(ctx, "Panduan-Pasang", "", []byte("Langkah 1, 2"), alice, "Panduan 2", c2); err != nil {
		t.Fatalf("an unrelated page from an older base: %v", err)
	}
	b, _, err := w.Read(ctx, Home, "")
	if err != nil || string(b) != "versi A" {
		t.Fatalf("read: %q %v", b, err)
	}
	old, _, _ := w.Read(ctx, Home, c1)
	if string(old) != "# Selamat datang\n" {
		t.Fatal("old revision")
	}
	ps, _ := w.Pages(ctx)
	if len(ps) != 2 || ps[0].Name != Home || ps[1].Title != "Panduan Pasang" {
		t.Fatalf("pages: %+v", ps)
	}
	h, _ := w.History(ctx, "Panduan-Pasang", 10)
	if len(h) != 2 || h[0].Subject != "Panduan 2" || h[0].Author != "alice" {
		t.Fatalf("history: %+v", h)
	}
	// rename keeps history in git, the old name disappears
	head, _ := w.Head(ctx)
	if _, err := w.Save(ctx, "Pemasangan", "Panduan-Pasang", []byte("Langkah 1, 2"), alice, "Nama baharu", head); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Read(ctx, "Panduan-Pasang", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("old name still there")
	}
	head, _ = w.Head(ctx)
	if _, err := w.Delete(ctx, "Pemasangan", alice, "Buang", head); err != nil {
		t.Fatal(err)
	}
	if ps, _ := w.Pages(ctx); len(ps) != 1 {
		t.Fatal("delete")
	}
	// the repository is real git: fsck clean, branch main
	if out, err := exec.Command("git", "-C", w.Path, "fsck", "--strict").CombinedOutput(); err != nil {
		t.Fatalf("fsck: %s", out)
	}
	// names: what Slug makes is the only thing accepted, so a name is always a safe file name and URL segment
	for in, want := range map[string]string{"Panduan Pasang": "Panduan-Pasang", "  Soalan Lazim? (FAQ) ": "Soalan-Lazim-FAQ", "../etc/passwd": "etc-passwd", "Kés 2": "Kés-2"} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "../x", "a/b", "-a", ".git", "a b", "_semua"} {
		if ValidName(bad) {
			t.Errorf("%q accepted as a name", bad)
		}
		if _, err := w.Save(ctx, bad, "", []byte("x"), alice, "x", ""); err == nil {
			t.Errorf("save as %q", bad)
		}
	}
	if !strings.HasSuffix(w.Path, ".wiki.git") {
		t.Fatal("path")
	}
}
