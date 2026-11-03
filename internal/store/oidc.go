package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// OIDCConfig is the single OpenID Connect provider row (D91). SecretSealed is sealed under auth.secret_key.
type OIDCConfig struct {
	Enabled      bool
	Name         string
	Issuer       string
	ClientID     string
	SecretSealed string
	Scopes       string
	UpdatedAt    string
}

// Ready reports whether sign-in through the provider can be offered.
func (c OIDCConfig) Ready() bool {
	return c.Enabled && c.Name != "" && c.Issuer != "" && c.ClientID != "" && c.SecretSealed != ""
}

// OIDCConfig reads the configuration row.
func (s *Store) OIDCConfig(ctx context.Context) (OIDCConfig, error) {
	var c OIDCConfig
	err := s.db.QueryRowContext(ctx, `SELECT enabled, name, issuer, client_id, secret_sealed, scopes, updated_at FROM oidc_config WHERE id = 1`).
		Scan(&c.Enabled, &c.Name, &c.Issuer, &c.ClientID, &c.SecretSealed, &c.Scopes, &c.UpdatedAt)
	return c, err
}

// SetOIDCConfig replaces the configuration row.
func (s *Store) SetOIDCConfig(ctx context.Context, c OIDCConfig) error {
	_, err := s.db.ExecContext(ctx, `UPDATE oidc_config SET enabled = ?, name = ?, issuer = ?, client_id = ?, secret_sealed = ?, scopes = ?, updated_at = ? WHERE id = 1`,
		c.Enabled, c.Name, c.Issuer, c.ClientID, c.SecretSealed, c.Scopes, Now())
	return err
}

// OIDCFlow is one sign-in in progress.
type OIDCFlow struct {
	State, Nonce, Verifier, Next string
}

func hashHex(v string) string {
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}

// StartOIDCFlow records a sign-in in progress; browser is the cookie value that must come back with it. Flows older
// than ttl are swept here.
func (s *Store) StartOIDCFlow(ctx context.Context, f OIDCFlow, browser string, ttl time.Duration) error {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM oidc_flows WHERE created_at < ?`, time.Now().Add(-ttl).UTC().Format(timeFormat))
	_, err := s.db.ExecContext(ctx, `INSERT INTO oidc_flows (state, browser_hash, nonce, verifier, next, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		f.State, hashHex(browser), f.Nonce, f.Verifier, f.Next, Now())
	return err
}

// TakeOIDCFlow returns and deletes the flow of state, only when browser matches and it is younger than ttl. Any
// mismatch deletes nothing and returns ErrNotFound, so a stolen state alone is useless and a flow is used once.
func (s *Store) TakeOIDCFlow(ctx context.Context, state, browser string, ttl time.Duration) (OIDCFlow, error) {
	var f OIDCFlow
	var bh, at string
	err := s.db.QueryRowContext(ctx, `SELECT state, browser_hash, nonce, verifier, next, created_at FROM oidc_flows WHERE state = ?`, state).
		Scan(&f.State, &bh, &f.Nonce, &f.Verifier, &f.Next, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	if err != nil {
		return f, err
	}
	t, _ := time.Parse(timeFormat, at)
	if browser == "" || bh != hashHex(browser) || time.Since(t) > ttl {
		return OIDCFlow{}, ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM oidc_flows WHERE state = ?`, state)
	if err != nil {
		return OIDCFlow{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 { // another request took it first
		return OIDCFlow{}, ErrNotFound
	}
	return f, nil
}

// OIDCLink is one provider identity linked to an account.
type OIDCLink struct {
	Issuer, Subject, Email, CreatedAt, LastUsedAt string
	UserID                                        int64
}

// OIDCUser returns the account linked to (issuer, subject).
func (s *Store) OIDCUser(ctx context.Context, issuer, subject string) (User, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM oidc_links WHERE issuer = ? AND subject = ?`, issuer, subject).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, id)
}

// LinkOIDC links (issuer, subject) to an account, or touches an existing link.
func (s *Store) LinkOIDC(ctx context.Context, issuer, subject string, userID int64, email string) error {
	now := Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO oidc_links (issuer, subject, user_id, email, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(issuer, subject) DO UPDATE SET email = excluded.email, last_used_at = excluded.last_used_at`, issuer, subject, userID, email, now, now)
	return err
}

// OIDCLinks lists an account's provider identities.
func (s *Store) OIDCLinks(ctx context.Context, userID int64) ([]OIDCLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT issuer, subject, user_id, email, created_at, COALESCE(last_used_at, '') FROM oidc_links WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OIDCLink
	for rows.Next() {
		var l OIDCLink
		if err := rows.Scan(&l.Issuer, &l.Subject, &l.UserID, &l.Email, &l.CreatedAt, &l.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// UnlinkOIDC removes one of an account's provider identities.
func (s *Store) UnlinkOIDC(ctx context.Context, userID int64, issuer, subject string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM oidc_links WHERE user_id = ? AND issuer = ? AND subject = ?`, userID, issuer, subject)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserByEmail finds the one active person (not an organisation) with this email, case-insensitive; two accounts
// sharing an address is ErrExists (ambiguous, never guessed).
func (s *Store) UserByEmail(ctx context.Context, email string) (User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return User{}, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM users WHERE lower(email) = lower(?) AND kind = 'user' AND disabled_at IS NULL LIMIT 2`, email)
	if err != nil {
		return User{}, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return User{}, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	switch len(ids) {
	case 0:
		return User{}, ErrNotFound
	case 1:
		return s.UserByID(ctx, ids[0])
	}
	return User{}, ErrExists
}
