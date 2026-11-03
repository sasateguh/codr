package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReaperState (D98): PID 1's name and the defunct children waiting on it, read from a /proc tree. A comm with
// spaces and parentheses must not shift the fields; a zombie under another parent, or a live child, is not counted.
func TestReaperState(t *testing.T) {
	proc := t.TempDir()
	write := func(pid, comm, stat string) {
		dir := filepath.Join(proc, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if comm != "" {
			_ = os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o600)
		}
		_ = os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o600)
	}
	write("1", "codr", "1 (codr) S 0 1 1 0")
	write("20", "", "20 (git) Z 1 20 20 0")
	write("21", "", "21 (odd) name (x)) Z 1 21 21 0")
	write("22", "", "22 (git) Z 7 22 22 0")             // someone else's zombie
	write("23", "", "23 (git) S 1 23 23 0")             // alive
	_ = os.MkdirAll(filepath.Join(proc, "self"), 0o755) // not a pid
	pid1, n, err := reaperState(proc)
	if err != nil || pid1 != "codr" || n != 2 {
		t.Fatalf("got %q %d %v, want codr 2", pid1, n, err)
	}
	if _, _, err := reaperState(filepath.Join(proc, "nope")); err == nil {
		t.Fatal("no /proc must be an error (the check is skipped, not passed)")
	}
}
