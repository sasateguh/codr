package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// User is a row of users.
type User struct {
	ID        int64
	Name      string
	Email     string
	IsAdmin   bool
	Lang      string
	Theme     string
	CreatedAt string
	Disabled  bool
	IsOrg     bool // an organisation (D74): a namespace, never an account that signs in
}

// SSHKey is a row of ssh_keys.
type SSHKey struct {
	ID          int64
	UserID      int64
	Title       string
	Fingerprint string
	PublicKey   string
	CreatedAt   string
}

// CreateUser inserts a user; passwordHash comes from auth.Hash.
func (s *Store) CreateUser(ctx context.Context, name, email, passwordHash string, isAdmin bool, lang string) (User, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (name, email, password_hash, is_admin, lang, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		name, nullIfEmpty(email), passwordHash, isAdmin, lang, now)
	if err != nil {
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Name: name, Email: email, IsAdmin: isAdmin, Lang: lang, Theme: "auto", CreatedAt: now}, nil
}

const userCols = `id, name, COALESCE(email,''), is_admin, lang, theme, created_at, disabled_at IS NOT NULL, kind = 'org'`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.IsAdmin, &u.Lang, &u.Theme, &u.CreatedAt, &u.Disabled, &u.IsOrg)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// UserByName looks a user up by exact name.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE name = ?`, name))
}

// UserByID looks a user up by id.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// PasswordHash returns the stored hash for login checks.
func (s *Store) PasswordHash(ctx context.Context, userID int64) (string, error) {
	var h string
	err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, userID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return h, err
}

// SetPasswordHash replaces the hash (password change or rehash).
func (s *Store) SetPasswordHash(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID)
	return err
}

// Users lists every user ordered by name.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// AddSSHKey registers a public key; fingerprint must be unique across all users.
func (s *Store) AddSSHKey(ctx context.Context, userID int64, title, fingerprint, publicKey string) (SSHKey, error) {
	if u, err := s.UserByID(ctx, userID); err == nil && u.IsOrg {
		return SSHKey{}, ErrIsOrg
	}
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys (user_id, title, fingerprint, public_key, created_at) VALUES (?, ?, ?, ?, ?)`,
		userID, title, fingerprint, publicKey, now)
	if err != nil {
		return SSHKey{}, err
	}
	id, _ := res.LastInsertId()
	return SSHKey{ID: id, UserID: userID, Title: title, Fingerprint: fingerprint, PublicKey: publicKey, CreatedAt: now}, nil
}

// SSHKeyByFingerprint resolves a presented key to its row (the SSH auth path).
func (s *Store) SSHKeyByFingerprint(ctx context.Context, fingerprint string) (SSHKey, error) {
	var k SSHKey
	err := s.db.QueryRowContext(ctx, `SELECT id, user_id, title, fingerprint, public_key, created_at FROM ssh_keys WHERE fingerprint = ?`, fingerprint).
		Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.PublicKey, &k.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SSHKey{}, ErrNotFound
	}
	return k, err
}

// SSHKeys lists a user's keys.
func (s *Store) SSHKeys(ctx context.Context, userID int64) ([]SSHKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_id, title, fingerprint, public_key, created_at FROM ssh_keys WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHKey
	for rows.Next() {
		var k SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.PublicKey, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// TouchSSHKey records a successful use.
func (s *Store) TouchSSHKey(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ssh_keys SET last_used_at = ? WHERE id = ?`, Now(), id)
	return err
}

// DeleteSSHKey removes one key of one user.
func (s *Store) DeleteSSHKey(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CreateSession mints a random token, stores only its sha256, and returns the token
// (the only time it is visible) plus its expiry.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, userAgent, ip string) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = hex.EncodeToString(raw)
	expires = time.Now().UTC().Add(ttl)
	_, err = s.db.ExecContext(ctx, `INSERT INTO sessions (id, user_id, created_at, expires_at, user_agent, ip) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID(token), userID, Now(), expires.Format("2006-01-02T15:04:05.000Z"), userAgent, ip)
	return token, expires, err
}

// SessionUser resolves a token to its user, or ErrNotFound if unknown/expired.
func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	var uid int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id = ? AND expires_at > ?`, sessionID(token), Now()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.UserByID(ctx, uid)
}

// DeleteSession logs a token out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID(token))
	return err
}

// PurgeSessions removes expired rows; returns how many.
func (s *Store) PurgeSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, Now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func sessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}
