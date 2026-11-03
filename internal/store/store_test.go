package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	v, n, err := s.Migrate(ctx)
	if err != nil || n != 0 || v < 1 {
		t.Fatalf("second migrate: v=%d n=%d err=%v", v, n, err)
	}
	cv, pending, err := s.Pending(ctx)
	if err != nil || pending != 0 || cv != v {
		t.Fatalf("pending: %d %d %v", cv, pending, err)
	}
	if name, err := s.Setting(ctx, "site.name"); err != nil || name == "" {
		t.Fatalf("seeded settings missing: %q %v", name, err)
	}
	if _, err := s.Setting(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("unknown setting: %v", err)
	}
	if err := s.SetSetting(ctx, "nope", "x", 0); err != ErrNotFound {
		t.Fatal("SetSetting must reject unknown keys")
	}
	if err := s.SetSetting(ctx, "site.name", "Forge", 0); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Setting(ctx, "site.name"); v != "Forge" {
		t.Fatal("SetSetting did not persist")
	}
	if b, err := s.SettingBool(ctx, "repo.default_private"); err != nil || !b {
		t.Fatal("repo.default_private should be true")
	}
	if n, err := s.SettingInt(ctx, "repo.push_max_mb"); err != nil || n <= 0 {
		t.Fatal("repo.push_max_mb should be a positive int")
	}
}

func TestUsersKeysSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "aiman", "", "hash", true, "ms-MY")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "aiman", "", "hash", false, "ms-MY"); err == nil {
		t.Fatal("duplicate user must fail")
	}
	got, err := s.UserByName(ctx, "aiman")
	if err != nil || got.ID != u.ID || !got.IsAdmin || got.Lang != "ms-MY" {
		t.Fatalf("UserByName: %+v %v", got, err)
	}
	if _, err := s.UserByName(ctx, "nobody"); err != ErrNotFound {
		t.Fatal("missing user must be ErrNotFound")
	}
	k, err := s.AddSSHKey(ctx, u.ID, "laptop", "SHA256:abc", "ssh-ed25519 AAAA laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSSHKey(ctx, u.ID, "again", "SHA256:abc", "ssh-ed25519 AAAA"); err == nil {
		t.Fatal("duplicate fingerprint must fail")
	}
	if k2, err := s.SSHKeyByFingerprint(ctx, "SHA256:abc"); err != nil || k2.ID != k.ID || k2.UserID != u.ID {
		t.Fatalf("SSHKeyByFingerprint: %+v %v", k2, err)
	}
	if err := s.TouchSSHKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSSHKey(ctx, u.ID+1, k.ID); err != ErrNotFound {
		t.Fatal("another user must not delete the key")
	}
	tok, exp, err := s.CreateSession(ctx, u.ID, time.Hour, "ua", "1.2.3.4")
	if err != nil || len(tok) != 64 || exp.Before(time.Now()) {
		t.Fatalf("CreateSession: %q %v %v", tok, exp, err)
	}
	if su, err := s.SessionUser(ctx, tok); err != nil || su.ID != u.ID {
		t.Fatalf("SessionUser: %+v %v", su, err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT id FROM sessions`).Scan(&stored); err != nil || stored == tok {
		t.Fatal("session id must be a hash of the token, not the token")
	}
	if _, err := s.SessionUser(ctx, "wrong"); err != ErrNotFound {
		t.Fatal("unknown token must be ErrNotFound")
	}
	old, _, _ := s.CreateSession(ctx, u.ID, -time.Minute, "", "")
	if _, err := s.SessionUser(ctx, old); err != ErrNotFound {
		t.Fatal("expired session must be ErrNotFound")
	}
	if n, err := s.PurgeSessions(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeSessions = %d %v", n, err)
	}
	if err := s.DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser(ctx, tok); err != ErrNotFound {
		t.Fatal("deleted session must be gone")
	}
}

func TestReposAndAccess(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	a, _ := s.CreateUser(ctx, "alice", "", "h", false, "en-US")
	b, _ := s.CreateUser(ctx, "bob", "", "h", false, "en-US")
	root, _ := s.CreateUser(ctx, "rootadmin", "", "h", true, "en-US")
	r, err := s.CreateRepo(ctx, a.ID, "proj", "desc", true, "main")
	if err != nil || r.Owner != "alice" || !r.Private {
		t.Fatalf("CreateRepo: %+v %v", r, err)
	}
	if _, err := s.CreateRepo(ctx, a.ID, "proj", "", true, "main"); err == nil {
		t.Fatal("duplicate owner/name must fail")
	}
	if err := s.SetDescription(ctx, r.ID, "baharu"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.RepoByID(ctx, r.ID); got.Description != "baharu" {
		t.Fatalf("SetDescription: %q", got.Description)
	}
	if n, err := s.RepoCount(ctx); err != nil || n != 1 {
		t.Fatalf("RepoCount: %d %v", n, err)
	}
	check := func(uid int64, want Access) {
		t.Helper()
		got, err := s.AccessFor(ctx, uid, r)
		if err != nil || got != want {
			t.Fatalf("AccessFor(%d) = %v %v, want %v", uid, got, err, want)
		}
	}
	check(a.ID, AccessAdmin)
	check(b.ID, AccessNone)
	check(0, AccessNone)
	check(root.ID, AccessAdmin)
	if err := s.SetCollaborator(ctx, r.ID, b.ID, AccessRead); err != nil {
		t.Fatal(err)
	}
	check(b.ID, AccessRead)
	if err := s.SetCollaborator(ctx, r.ID, b.ID, AccessWrite); err != nil {
		t.Fatal(err)
	}
	check(b.ID, AccessWrite)
	if err := s.RemoveCollaborator(ctx, r.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	check(b.ID, AccessNone)
	pub, _ := s.CreateRepo(ctx, a.ID, "open", "", false, "main")
	if got, _ := s.AccessFor(ctx, 0, pub); got != AccessRead {
		t.Fatal("anonymous must read a public repo")
	}
	if got, _ := s.AccessFor(ctx, b.ID, pub); got != AccessRead {
		t.Fatal("other user must read a public repo, not write")
	}
	if err := s.SetArchived(ctx, r.ID, true); err != nil {
		t.Fatal(err)
	}
	if r2, _ := s.RepoByPath(ctx, "alice", "proj"); !r2.Archived {
		t.Fatal("archive flag not persisted")
	}
	list, _ := s.Repos(ctx, "alice")
	if len(list) != 2 {
		t.Fatalf("Repos(alice) = %d", len(list))
	}
	if err := s.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepoByID(ctx, r.ID); err != ErrNotFound {
		t.Fatal("deleted repo must be gone")
	}
}

func TestAuditIsAppendOnly(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Audit(ctx, 0, "test", "x", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE audit_log SET action = 'y'`); err == nil {
		t.Fatal("UPDATE on audit_log must be refused by trigger")
	}
	if _, err := s.db.Exec(`DELETE FROM audit_log`); err == nil {
		t.Fatal("DELETE on audit_log must be refused by trigger")
	}
}

func TestIssuesLabelsComments(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "alice", "", "x", false, "ms-MY")
	r, _ := s.CreateRepo(ctx, u.ID, "p", "", true, "main")
	i1, err := s.CreateIssue(ctx, r.ID, u.ID, "Pertama", "badan")
	if err != nil || i1.Number != 1 || i1.State != "open" || i1.Author != "alice" {
		t.Fatalf("create: %+v %v", i1, err)
	}
	i2, _ := s.CreateIssue(ctx, r.ID, u.ID, "Kedua", "")
	if i2.Number != 2 {
		t.Fatalf("numbering: %d", i2.Number)
	}
	if _, err := s.IssueByNumber(ctx, r.ID, 9); err != ErrNotFound {
		t.Fatalf("missing issue: %v", err)
	}
	l, err := s.CreateLabel(ctx, r.ID, "pepijat", "h3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLabel(ctx, r.ID, "Pepijat", "h1"); err != ErrExists {
		t.Fatalf("duplicate label (case-insensitive): %v", err)
	}
	if err := s.SetIssueLabels(ctx, r.ID, i1.ID, []int64{l.ID, 999}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.IssueByID(ctx, i1.ID)
	if len(got.Labels) != 1 || got.Labels[0].Name != "pepijat" {
		t.Fatalf("labels: %+v", got.Labels)
	}
	if _, err := s.AddComment(ctx, i1.ID, u.ID, "komen"); err != nil {
		t.Fatal(err)
	}
	list, total, err := s.Issues(ctx, r.ID, "open", l.ID, "", 0, 10, 0)
	if err != nil || total != 1 || list[0].Number != 1 || list[0].Comments != 1 {
		t.Fatalf("filter by label: %d %v %+v", total, err, list)
	}
	if _, total, _ := s.Issues(ctx, r.ID, "open", 0, "KEDUA", 0, 10, 0); total != 1 {
		t.Fatalf("title search: %d", total)
	}
	if err := s.SetIssueState(ctx, i2.ID, true); err != nil {
		t.Fatal(err)
	}
	open, closed, _ := s.IssueCounts(ctx, r.ID)
	if open != 1 || closed != 1 {
		t.Fatalf("counts: %d %d", open, closed)
	}
	if err := s.UpdateIssue(ctx, i1.ID, "Pertama (disunting)", "badan baharu"); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.Labels(ctx, r.ID)
	if len(ls) != 1 || ls[0].Open != 1 {
		t.Fatalf("label open count: %+v", ls)
	}
	if err := s.DeleteLabel(ctx, r.ID+1, l.ID); err != ErrNotFound {
		t.Fatalf("delete label of another repo: %v", err)
	}
	if err := s.DeleteLabel(ctx, r.ID, l.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.IssueByID(ctx, i1.ID); len(got.Labels) != 0 || got.Title != "Pertama (disunting)" {
		t.Fatalf("after label delete: %+v", got)
	}
	if err := s.DeleteRepo(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueByID(ctx, i1.ID); err != ErrNotFound {
		t.Fatalf("issues must cascade with the repo: %v", err)
	}
}

// TestPersonalDataHasNoSecrets: the export carries the person's records and none of the hashes (D73).
func TestPersonalDataHasNoSecrets(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "siti", "siti@example.invalid", "HASH-RAHSIA", false, "ms-MY")
	raw, _, _ := s.CreateToken(ctx, u.ID, "laptop", []string{"repo:read"}, time.Hour)
	r, _ := s.CreateRepo(ctx, u.ID, "kertas", "", true, "main")
	_, _ = s.CreateIssue(ctx, r.ID, u.ID, "Isu saya", "badan")
	d, err := s.PersonalData(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	for _, want := range []string{"siti@example.invalid", "laptop", "kertas", "Isu saya"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("export lacks %q", want)
		}
	}
	if strings.Contains(string(b), "HASH-RAHSIA") || strings.Contains(string(b), raw) || strings.Contains(string(b), "token_hash") {
		t.Fatal("a secret reached the export")
	}
	var csvOut strings.Builder
	_ = s.Audit(ctx, u.ID, "x.y", "t", "d,\"q\"")
	if err := s.WriteAuditCSV(ctx, &csvOut); err != nil || !strings.Contains(csvOut.String(), `"d,""q"""`) {
		t.Fatalf("csv: %v %s", err, csvOut.String())
	}
}
