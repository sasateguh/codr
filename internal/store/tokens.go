package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"time"
)

// Token scopes. repo:write implies repo:read; admin implies both (reserved for the API).
const (
	ScopeRead  = "repo:read"
	ScopeWrite = "repo:write"
	ScopeAdmin = "admin"
)

// TokenPrefix marks Codr tokens so secret scanners can recognise them. Tokens issued before the rename
// (D58) carry LegacyTokenPrefix and keep working: the hash covers the whole string, prefix included.
const (
	TokenPrefix       = "codr_"
	LegacyTokenPrefix = "amcd_"
)

// AccessToken is a row of access_tokens (hash never exposed).
type AccessToken struct {
	ID         int64
	UserID     int64
	Name       string
	Scopes     []string
	CreatedAt  string
	ExpiresAt  string
	LastUsedAt string
}

// ParseScopes validates and normalises a comma list of scopes.
func ParseScopes(list string) ([]string, bool) {
	seen := map[string]bool{}
	for _, s := range strings.Split(list, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if s != ScopeRead && s != ScopeWrite && s != ScopeAdmin {
			return nil, false
		}
		seen[s] = true
	}
	if len(seen) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, true
}

// Allows reports whether the token's scopes cover the access level asked for.
func (t AccessToken) Allows(a Access) bool {
	has := func(s string) bool {
		for _, x := range t.Scopes {
			if x == s {
				return true
			}
		}
		return false
	}
	switch {
	case a <= AccessNone:
		return true
	case a == AccessRead:
		return has(ScopeRead) || has(ScopeWrite) || has(ScopeAdmin)
	case a == AccessWrite:
		return has(ScopeWrite) || has(ScopeAdmin)
	}
	return has(ScopeAdmin)
}

// CreateToken mints a token, stores only its hash, and returns the raw token (visible once).
func (s *Store) CreateToken(ctx context.Context, userID int64, name string, scopes []string, ttl time.Duration) (raw string, t AccessToken, err error) {
	if u, err := s.UserByID(ctx, userID); err == nil && u.IsOrg {
		return "", AccessToken{}, ErrIsOrg
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", AccessToken{}, err
	}
	raw = TokenPrefix + hex.EncodeToString(b)
	now := Now()
	exp := time.Now().UTC().Add(ttl).Format("2006-01-02T15:04:05.000Z")
	res, err := s.db.ExecContext(ctx, `INSERT INTO access_tokens (user_id, name, token_hash, scopes, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		userID, name, tokenHash(raw), strings.Join(scopes, ","), now, exp)
	if err != nil {
		return "", AccessToken{}, err
	}
	id, _ := res.LastInsertId()
	return raw, AccessToken{ID: id, UserID: userID, Name: name, Scopes: scopes, CreatedAt: now, ExpiresAt: exp}, nil
}

const atCols = `id, user_id, name, scopes, created_at, expires_at, COALESCE(last_used_at,'')` // #nosec G101 -- SQL column list, not a credential

func scanToken(row interface{ Scan(...any) error }) (AccessToken, error) {
	var t AccessToken
	var scopes string
	err := row.Scan(&t.ID, &t.UserID, &t.Name, &scopes, &t.CreatedAt, &t.ExpiresAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessToken{}, ErrNotFound
	}
	t.Scopes = strings.Split(scopes, ",")
	return t, err
}

// TokenUser resolves a raw token to its unexpired row and user, or ErrNotFound.
// A disabled user's tokens are treated as unknown.
func (s *Store) TokenUser(ctx context.Context, raw string) (AccessToken, User, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+atCols+` FROM access_tokens WHERE token_hash = ? AND expires_at > ?`, tokenHash(raw), Now()))
	if err != nil {
		return AccessToken{}, User{}, err
	}
	u, err := s.UserByID(ctx, t.UserID)
	if err != nil {
		return AccessToken{}, User{}, err
	}
	if u.Disabled || u.IsOrg {
		return AccessToken{}, User{}, ErrNotFound
	}
	return t, u, nil
}

// Tokens lists a user's tokens, newest first.
func (s *Store) Tokens(ctx context.Context, userID int64) ([]AccessToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+atCols+` FROM access_tokens WHERE user_id = ? ORDER BY id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchToken records a successful use.
func (s *Store) TouchToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE access_tokens SET last_used_at = ? WHERE id = ?`, Now(), id)
	return err
}

// DeleteToken revokes one token of one user.
func (s *Store) DeleteToken(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM access_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func tokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
