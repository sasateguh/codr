package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokensAndProtections(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	u, _ := st.CreateUser(ctx, "alice", "", "x", false, "ms-MY")
	if _, ok := ParseScopes("repo:bogus"); ok {
		t.Fatal("bogus scope accepted")
	}
	scopes, _ := ParseScopes("repo:write, repo:read")
	raw, tok, err := st.CreateToken(ctx, u.ID, "laptop", scopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) != len(TokenPrefix)+64 {
		t.Fatalf("token shape %q", raw)
	}
	got, gu, err := st.TokenUser(ctx, raw)
	if err != nil || got.ID != tok.ID || gu.ID != u.ID {
		t.Fatalf("TokenUser: %v %+v", err, got)
	}
	if !got.Allows(AccessWrite) || got.Allows(AccessAdmin) {
		t.Fatalf("scopes wrong: %v", got.Scopes)
	}
	if _, _, err := st.TokenUser(ctx, raw+"x"); err != ErrNotFound {
		t.Fatalf("wrong token should be unknown, got %v", err)
	}
	// The hash is what is stored: the raw token never appears in the DB file.
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM access_tokens WHERE token_hash = ?`, raw).Scan(&n); err != nil || n != 0 {
		t.Fatal("raw token stored")
	}
	// Expired tokens are unknown.
	rawOld, _, _ := st.CreateToken(ctx, u.ID, "old", scopes, -time.Minute)
	if _, _, err := st.TokenUser(ctx, rawOld); err != ErrNotFound {
		t.Fatal("expired token accepted")
	}
	list, _ := st.Tokens(ctx, u.ID)
	if len(list) != 2 {
		t.Fatalf("tokens = %d", len(list))
	}
	if err := st.DeleteToken(ctx, u.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.TokenUser(ctx, raw); err != ErrNotFound {
		t.Fatal("revoked token accepted")
	}

	r, _ := st.CreateRepo(ctx, u.ID, "r", "", true, "main")
	rule, _ := st.RuleFor(ctx, r, "main")
	if !rule.Protected || rule.AllowForce || rule.AllowDelete {
		t.Fatalf("default branch should be protected by setting: %+v", rule)
	}
	rule, _ = st.RuleFor(ctx, r, "feature")
	if rule.Protected {
		t.Fatal("feature protected without rule")
	}
	if err := st.Protect(ctx, r.ID, "release/*", true, false, false); err != nil {
		t.Fatal(err)
	}
	rule, _ = st.RuleFor(ctx, r, "release/1.0")
	if !rule.Protected || !rule.AllowForce || rule.AllowDelete {
		t.Fatalf("release rule: %+v", rule)
	}
	if err := st.SetSetting(ctx, "repo.protect_default_branch", "false", u.ID); err != nil {
		t.Fatal(err)
	}
	rule, _ = st.RuleFor(ctx, r, "main")
	if rule.Protected {
		t.Fatal("setting off but main still protected")
	}
	if err := st.Protect(ctx, r.ID, "[", false, false, false); err == nil {
		t.Fatal("bad glob accepted")
	}
	if err := st.Unprotect(ctx, r.ID, "release/*"); err != nil {
		t.Fatal(err)
	}
	if err := st.Unprotect(ctx, r.ID, "release/*"); err != ErrNotFound {
		t.Fatal("second unprotect should be not found")
	}
}

// TestLegacyTokenPrefix: a token issued before the rename (amcd_, D58) still resolves; new ones carry codr_.
func TestLegacyTokenPrefix(t *testing.T) {
	st := open(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, "lama", "", "x", false, "ms-MY")
	if err != nil {
		t.Fatal(err)
	}
	raw, tok, err := st.CreateToken(ctx, u.ID, "t", []string{ScopeRead}, time.Hour)
	if err != nil || !strings.HasPrefix(raw, TokenPrefix) {
		t.Fatalf("new token %q %v", raw, err)
	}
	legacy := LegacyTokenPrefix + strings.TrimPrefix(raw, TokenPrefix)
	if _, err := st.db.ExecContext(ctx, `UPDATE access_tokens SET token_hash = ? WHERE id = ?`, tokenHash(legacy), tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, got, err := st.TokenUser(ctx, legacy); err != nil || got.ID != u.ID {
		t.Fatalf("legacy token: %v", err)
	}
}
