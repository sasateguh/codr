package gitread

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestSquashAndRebase (D88): squash = one commit with the merged tree and the given author; rebase = each commit replayed
// with its own author, date and message on top of base; conflicts, merge commits, the replay limit and a moved base
// (compare-and-swap) all leave the branch where it was.
func TestSquashAndRebase(t *testing.T) {
	r, run := mergeFixture(t)
	ctx := context.Background()
	mainID, _ := r.Resolve(ctx, "main")
	me := Ident{Name: "Penggabung", Email: "gabung@example.invalid"}

	// rebase: refused when stale, conflicting, or too long; branch untouched each time
	if _, _, err := r.Rebase(ctx, "main", "ciri", strings.Repeat("0", 40), me, 0); err == nil {
		t.Fatal("stale base accepted")
	}
	if _, paths, err := r.Rebase(ctx, "main", "konflik", mainID, me, 0); !errors.Is(err, ErrConflict) || len(paths) != 1 || paths[0] != "a.txt" {
		t.Fatalf("conflict: %v %v", paths, err)
	}
	if _, _, err := r.Rebase(ctx, "main", "ciri", mainID, me, 1); !errors.Is(err, ErrTooMany) {
		t.Fatalf("limit: %v", err)
	}
	if now, _ := r.Resolve(ctx, "main"); now != mainID {
		t.Fatal("a refused rebase moved main")
	}
	orig, _ := r.LogRange(ctx, "main", "ciri", 10)
	tip, _, err := r.Rebase(ctx, "main", "ciri", mainID, me, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Log(ctx, "main", "", 3, 0)
	if got[0].Hash != tip || len(got[0].Parents) != 1 || got[2].Hash != mainID {
		t.Fatalf("rebase is not linear on main: %+v", got)
	}
	for i := 0; i < 2; i++ {
		if got[i].Subject != orig[i].Subject || got[i].Author != orig[i].Author || !got[i].When.Equal(orig[i].When) || got[i].Hash == orig[i].Hash {
			t.Fatalf("replayed commit %d lost its identity: %+v vs %+v", i, got[i], orig[i])
		}
	}
	if b, err := r.Blob(ctx, "main", "a.txt", 1<<10); err != nil || string(b.Content) != "tiga\n" {
		t.Fatal("main's own change lost")
	}
	if b, err := r.Blob(ctx, "main", "c.txt", 1<<10); err != nil || string(b.Content) != "c\ncc\n" {
		t.Fatal("replayed content missing")
	}
	if c, _ := r.Commit(ctx, tip); c.Committer != "Penggabung" {
		t.Fatalf("committer: %q", c.Committer)
	}

	// squash: one commit, merged tree, chosen author; a merge commit in head is refused by rebase but fine for squash
	run("checkout", "-q", "-b", "gabungan", "ciri")
	run("merge", "-q", "--no-ff", "-m", "merge konflik", "konflik")
	run("push", "-q", r.Path, "gabungan")
	base, _ := r.Resolve(ctx, "main")
	if _, _, err := r.Rebase(ctx, "main", "gabungan", base, me, 0); !errors.Is(err, ErrNotLinear) {
		t.Fatalf("merge commit in head: %v", err)
	}
	run("checkout", "-q", "-b", "kecil", "ciri")
	run("rm", "-q", "b.txt")
	run("commit", "-q", "-m", "buang b")
	run("push", "-q", r.Path, "kecil")
	sq, err := r.Squash(ctx, "main", "kecil", base, Ident{Name: "Pengarang PR", Email: "pr@example.invalid"}, me, "Buang b (#4)\n")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := r.Commit(ctx, "main")
	if c.Hash != sq || len(c.Parents) != 1 || c.Parents[0] != base || c.Author != "Pengarang PR" || c.Committer != "Penggabung" || c.Subject != "Buang b (#4)" {
		t.Fatalf("squash commit: %+v", c)
	}
	if _, err := r.Blob(ctx, "main", "b.txt", 1<<10); err == nil {
		t.Fatal("squashed change (b.txt removed) missing")
	}
	if _, err := r.Squash(ctx, "main", "kecil", base, me, me, "again"); err == nil {
		t.Fatal("squash with a stale base accepted")
	}
}
