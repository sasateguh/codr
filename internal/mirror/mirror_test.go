package mirror

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codr.my/codr/internal/repos"
	"codr.my/codr/internal/secret"
	"codr.my/codr/internal/store"
)

func TestValidURL(t *testing.T) {
	for _, ok := range []string{"https://github.com/aimanmalib/x.git", "http://127.0.0.1:3000/a/b.git", "ssh://git@host:2222/a/b.git", "git@github.com:a/b.git"} {
		if err := ValidURL(ok, false); err != nil {
			t.Errorf("%s refused", ok)
		}
	}
	for _, bad := range []string{"", "-x", "https://user:pw@github.com/a/b", "file:///tmp/x", "/tmp/x", "https://h/a b", "ftp://h/x", "https://h/$(x)", "https://h/x'y"} {
		if err := ValidURL(bad, false); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := ValidURL("file:///tmp/x", true); err != nil {
		t.Error("local allowed for CLI")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=master", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSyncFollowsSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
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
	u, _ := st.CreateUser(ctx, "alice", "", "x", false, "en-US")
	src := filepath.Join(dir, "src")
	_ = os.MkdirAll(src, 0o700)
	run(t, src, "init", "-q")
	_ = os.WriteFile(filepath.Join(src, "a"), []byte("1\n"), 0o600)
	run(t, src, "add", ".")
	run(t, src, "commit", "-q", "-m", "satu")
	run(t, src, "tag", "v1")
	run(t, src, "branch", "lama")
	disk := repos.Disk{Root: filepath.Join(dir, "repos"), GitBinary: "git"}
	r, _ := st.CreateRepo(ctx, u.ID, "cermin", "", true, "main")
	if err := disk.Create(ctx, r.ID, "main"); err != nil {
		t.Fatal(err)
	}
	keyHex, _ := secret.Generate()
	key, _ := secret.Parse(keyHex)
	enc, _ := key.Seal("rahsia-token")
	if err := st.SetMirror(ctx, r.ID, "file://"+src, enc, 30); err != nil {
		t.Fatal(err)
	}
	if rp, _ := st.RepoByID(ctx, r.ID); !rp.Mirror {
		t.Fatal("Mirror flag not derived")
	}
	s := &Syncer{Store: st, Disk: disk, Git: "git", Timeout: 30 * time.Second, Key: key, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if err := s.Sync(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	bare := disk.Path(r.ID)
	if run(t, bare, "symbolic-ref", "--short", "HEAD") != "master" {
		t.Fatal("HEAD did not follow the source's default branch")
	}
	if rp, _ := st.RepoByID(ctx, r.ID); rp.DefaultBranch != "master" {
		t.Fatal("DB default_branch not updated")
	}
	if !strings.Contains(run(t, bare, "for-each-ref", "--format=%(refname)"), "refs/tags/v1") {
		t.Fatal("tag not mirrored")
	}
	m, _ := st.MirrorOf(ctx, r.ID)
	if m.LastOKAt == "" || m.LastError != "" {
		t.Fatalf("mirror row: %+v", m)
	}
	// source moves on: new commit, branch deleted, force-pushed history → mirror follows exactly
	_ = os.WriteFile(filepath.Join(src, "b"), []byte("2\n"), 0o600)
	run(t, src, "add", ".")
	run(t, src, "commit", "-q", "--amend", "-m", "satu dan dua")
	run(t, src, "branch", "-D", "lama")
	if err := s.Sync(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := run(t, bare, "rev-parse", "master"), run(t, src, "rev-parse", "master"); got != want {
		t.Fatalf("mirror %s != source %s", got, want)
	}
	if strings.Contains(run(t, bare, "for-each-ref", "--format=%(refname)"), "refs/heads/lama") {
		t.Fatal("deleted branch not pruned")
	}
	// a dead source records the error on the row (and the token is not in it), scheduler skips nothing
	_ = st.SetMirror(ctx, r.ID, "file://"+filepath.Join(dir, "tiada"), enc, 30)
	if err := s.Sync(ctx, r.ID); err == nil {
		t.Fatal("dead source should fail")
	}
	m, _ = st.MirrorOf(ctx, r.ID)
	if m.LastError == "" || strings.Contains(m.LastError, "rahsia-token") {
		t.Fatalf("error not recorded safely: %q", m.LastError)
	}
	due, _ := st.MirrorsDue(ctx)
	if len(due) != 0 {
		t.Fatal("just-synced mirror should not be due")
	}
	if _, err := st.DB().Exec(`UPDATE repo_mirrors SET last_sync_at = '2020-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if due, _ = st.MirrorsDue(ctx); len(due) != 1 {
		t.Fatal("stale mirror should be due")
	}
	var n int
	_ = st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action='mirror.sync' AND target='alice/cermin'`).Scan(&n)
	if n != 3 {
		t.Fatalf("audit rows %d", n)
	}
}
