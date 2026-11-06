package hooks

import (
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codr.my/codr/internal/store"
)

// TestRegresiSecondListenKeepsLiveSocket (5 Nov 2026): a second Listen on the path of a live server
// must fail and leave the path alone, so pushes keep reaching the first server. The old Listen removed
// the file and bound a new one; the live server kept an unnamed socket and every push failed.
func TestRegresiSecondListenKeepsLiveSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "codr-sock") // short path: Unix socket paths are limited to 108 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "hook.sock")
	lnA, err := Listen(p)
	if err != nil {
		t.Fatal(err)
	}
	a := &Server{Store: st, GitBinary: "git", Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = a.Serve(ctx, lnA) }()
	defer func() { cancel(); <-done }()

	lnB, err := Listen(p)
	if err == nil {
		lnB.Close()
		t.Error("second Listen on a live socket succeeded; it must return an error and leave the path alone")
	}
	// repo 0 does not exist: A answers "not OK" — what matters is that it answers at all
	if _, err := Call(p, Request{Hook: "pre-receive", Lang: "en-US"}, 2*time.Second); err != nil {
		t.Fatalf("Call after the second Listen no longer reaches the live server: %v", err)
	}
}

// TestRegresiDeadSocketFileIsReplaced: a socket file whose listener is gone (a serve that died without
// cleaning up) must not block the next start.
func TestRegresiDeadSocketFileIsReplaced(t *testing.T) {
	dir, err := os.MkdirTemp("", "codr-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "hook.sock")
	dead, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("fixture: dead socket file missing: %v", err)
	}
	ln, err := Listen(p)
	if err != nil {
		t.Fatalf("Listen over a dead socket file: %v", err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			c.Close()
		}
	}()
	c, err := net.DialTimeout("unix", p, 2*time.Second)
	if err != nil {
		t.Fatalf("the new socket does not answer: %v", err)
	}
	c.Close()
}
