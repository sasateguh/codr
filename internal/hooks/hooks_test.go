package hooks

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/store"
)

func TestParseRefsAndFlags(t *testing.T) {
	refs, err := ParseRefs(strings.NewReader("0000000000000000000000000000000000000000 abc refs/heads/main\nabc 0000000000000000000000000000000000000000 refs/heads/x\n"))
	if err != nil || len(refs) != 2 {
		t.Fatal(err, refs)
	}
	if !refs[0].Created() || refs[0].Deleted() || !refs[1].Deleted() {
		t.Fatalf("flags wrong: %+v", refs)
	}
	if _, err := ParseRefs(strings.NewReader("one two\n")); err == nil {
		t.Fatal("bad line accepted")
	}
}

func TestRunHookWithoutSocketAllows(t *testing.T) {
	var errb bytes.Buffer
	if code := RunHook("pre-receive", strings.NewReader(""), &errb, func(string) string { return "" }); code != 0 {
		t.Fatalf("code %d", code)
	}
	if code := RunHook("update", strings.NewReader(""), &errb, func(string) string { return "" }); code != 2 {
		t.Fatal("unknown hook name accepted")
	}
}

func TestPreReceiveFailsClosedWhenServeIsDown(t *testing.T) {
	var errb bytes.Buffer
	env := map[string]string{"CODR_SOCKET": filepath.Join(t.TempDir(), "none.sock"), "CODR_LANG": "en-US"}
	get := func(k string) string { return env[k] }
	if code := RunHook("pre-receive", strings.NewReader("a b refs/heads/main\n"), &errb, get); code != 1 || !strings.Contains(errb.String(), "hook failed") {
		t.Fatalf("pre-receive must fail closed: %d %q", code, errb.String())
	}
	// the cause travels to the pusher's terminal and the hos log (tolak-codr), not just "hook failed"
	if !strings.Contains(errb.String(), "Codr: cause: dial unix") || !strings.Contains(errb.String(), "no such file") {
		t.Fatalf("pre-receive must print why: %q", errb.String())
	}
	if code := RunHook("post-receive", strings.NewReader("a b refs/heads/main\n"), &errb, get); code != 0 {
		t.Fatal("post-receive must never block")
	}
}

func TestWriteScripts(t *testing.T) {
	dir := t.TempDir()
	if err := WriteScripts(dir, "relative/amcd"); err == nil {
		t.Fatal("relative binary accepted")
	}
	if err := WriteScripts(dir, "/opt/a'b/amcd"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "pre-receive"))
	if !strings.Contains(string(raw), `'/opt/a'\''b/amcd' hook pre-receive`) {
		t.Fatalf("script: %s", raw)
	}
	if st, _ := os.Stat(filepath.Join(dir, "post-receive")); st.Mode().Perm() != 0o700 {
		t.Fatalf("mode %o", st.Mode().Perm())
	}
}

func TestHandleProtectedBranchAndSize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(ctx, "alice", "", "x", false, "ms-MY")
	r, _ := st.CreateRepo(ctx, u.ID, "p", "", true, "main")
	srv := &Server{Store: st, Disk: repos.Disk{Root: filepath.Join(dir, "repos"), GitBinary: "git"}, GitBinary: "git", Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	zero := strings.Repeat("0", 40)
	del := Request{Hook: "pre-receive", RepoID: r.ID, UserID: u.ID, Lang: "ms-MY", Refs: []Ref{{Old: strings.Repeat("a", 40), New: zero, Name: "refs/heads/main"}}}
	if resp := srv.Handle(ctx, del); resp.OK || !strings.Contains(resp.Message, "tidak boleh dipadam") {
		t.Fatalf("delete of main: %+v", resp)
	}
	del.Refs[0].Name = "refs/heads/scratch"
	if resp := srv.Handle(ctx, del); !resp.OK {
		t.Fatalf("delete of unprotected branch refused: %+v", resp)
	}
	// size: a fake quarantine dir of 3 MB against a 2 MB limit
	q := filepath.Join(dir, "quarantine")
	_ = os.MkdirAll(q, 0o700)
	if err := os.WriteFile(filepath.Join(q, "pack"), make([]byte, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = st.SetSetting(ctx, "repo.push_max_mb", "2", u.ID)
	big := Request{Hook: "pre-receive", RepoID: r.ID, UserID: u.ID, Lang: "en-US", Env: map[string]string{"GIT_QUARANTINE_PATH": q},
		Refs: []Ref{{Old: zero, New: strings.Repeat("a", 40), Name: "refs/heads/feature"}}}
	if resp := srv.Handle(ctx, big); resp.OK || !strings.Contains(resp.Message, "3 MB, limit 2 MB") {
		t.Fatalf("size: %+v", resp)
	}
	_ = st.SetSetting(ctx, "repo.push_max_mb", "4", u.ID)
	if resp := srv.Handle(ctx, big); !resp.OK {
		t.Fatalf("under limit refused: %+v", resp)
	}
	// unknown repo = internal, never OK
	if resp := srv.Handle(ctx, Request{Hook: "pre-receive", RepoID: 999, Lang: "en-US"}); resp.OK {
		t.Fatal("unknown repo accepted")
	}
	// post-receive writes the audit row
	post := Request{Hook: "post-receive", RepoID: r.ID, UserID: u.ID, Door: "ssh", Refs: big.Refs}
	if resp := srv.Handle(ctx, post); !resp.OK {
		t.Fatal(resp)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action='git.push' AND target='alice/p' AND detail LIKE 'ssh refs/heads/feature%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("audit rows %d", n)
	}
	// socket round trip
	sock := filepath.Join(dir, "h.sock")
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(sctx, ln) }()
	resp, err := Call(sock, del, 5*time.Second)
	if err != nil || !resp.OK {
		t.Fatalf("call: %v %+v", err, resp)
	}
	cancel()
	<-done
}
