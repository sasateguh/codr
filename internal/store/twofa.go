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

// Two-step sign-in (D80).

// TwoFactor is a person's TOTP state. Sealed is set during setup; On is when a code confirmed it ("" = off).
type TwoFactor struct {
	Sealed   string
	On       string
	LastStep int64
	Unused   int // recovery codes not yet used
}

// TwoFactor reads the state.
func (s *Store) TwoFactor(ctx context.Context, userID int64) (TwoFactor, error) {
	var t TwoFactor
	err := s.db.QueryRowContext(ctx, `SELECT totp_sealed, COALESCE(totp_on_at, ''), totp_last_step,
		(SELECT COUNT(*) FROM recovery_codes WHERE user_id = users.id AND used_at IS NULL) FROM users WHERE id = ?`, userID).
		Scan(&t.Sealed, &t.On, &t.LastStep, &t.Unused)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// SetTOTPPending stores a new, not yet confirmed secret (setup started). It never switches 2FA off by itself: while
// 2FA is on, the old secret keeps working until the new one is confirmed.
func (s *Store) SetTOTPPending(ctx context.Context, userID int64, sealed string) error {
	return oneRow(s.db.ExecContext(ctx, `UPDATE users SET totp_sealed = ? WHERE id = ? AND totp_on_at IS NULL`, sealed, userID))
}

// EnableTOTP confirms the pending secret at the step of the code that confirmed it, and replaces the recovery codes.
func (s *Store) EnableTOTP(ctx context.Context, userID, step int64, codeHashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := oneRow(tx.ExecContext(ctx, `UPDATE users SET totp_on_at = ?, totp_last_step = ? WHERE id = ? AND totp_sealed != '' AND totp_on_at IS NULL`, Now(), step, userID)); err != nil {
		return err
	}
	if err := replaceRecovery(ctx, tx, userID, codeHashes); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceRecovery(ctx context.Context, tx *sql.Tx, userID int64, hashes []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := Now()
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)`, userID, h, now); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceRecoveryCodes issues a new set (the old ones stop working).
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceRecovery(ctx, tx, userID, hashes); err != nil {
		return err
	}
	return tx.Commit()
}

// DisableTOTP switches 2FA off: secret, recovery codes and pending challenges go.
func (s *Store) DisableTOTP(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{`UPDATE users SET totp_sealed = '', totp_on_at = NULL, totp_last_step = 0 WHERE id = ?`,
		`DELETE FROM recovery_codes WHERE user_id = ?`, `DELETE FROM login_challenges WHERE user_id = ?`} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseTOTPStep records an accepted step; it fails when an equal or later step was already used (a replay racing).
func (s *Store) UseTOTPStep(ctx context.Context, userID, step int64) error {
	return oneRow(s.db.ExecContext(ctx, `UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, step, userID, step))
}

// UseRecoveryCode spends one recovery code (by its hash); ErrNotFound when it is unknown or used.
func (s *Store) UseRecoveryCode(ctx context.Context, userID int64, hash string) error {
	return oneRow(s.db.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL`, Now(), userID, hash))
}

// RecoveryHash is how a recovery code is stored.
func RecoveryHash(normalized string) string {
	h := sha256.Sum256([]byte("codr-recovery:" + normalized))
	return hex.EncodeToString(h[:])
}

// Challenge is a sign-in between the password and the code.
type Challenge struct {
	UserID   int64
	Next     string
	Attempts int
}

// CreateChallenge starts a challenge and returns its token (only its sha256 is stored).
func (s *Store) CreateChallenge(ctx context.Context, userID int64, next, ip string, ttl time.Duration) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	_, err := s.db.ExecContext(ctx, `INSERT INTO login_challenges (id, user_id, next, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID(token), userID, next, ip, Now(), time.Now().UTC().Add(ttl).Format("2006-01-02T15:04:05.000Z"))
	return token, err
}

// TakeChallengeAttempt counts one attempt and returns the challenge; ErrNotFound when unknown, expired or used up.
func (s *Store) TakeChallengeAttempt(ctx context.Context, token string, maxAttempts int) (Challenge, error) {
	var c Challenge
	id := sessionID(token)
	if err := oneRow(s.db.ExecContext(ctx, `UPDATE login_challenges SET attempts = attempts + 1 WHERE id = ? AND expires_at > ? AND attempts < ?`, id, Now(), maxAttempts)); err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ?`, id)
		return c, ErrNotFound
	}
	err := s.db.QueryRowContext(ctx, `SELECT user_id, next, attempts FROM login_challenges WHERE id = ?`, id).Scan(&c.UserID, &c.Next, &c.Attempts)
	return c, err
}

// ChallengeValid tells whether a challenge token can still be answered (for the code page).
func (s *Store) ChallengeValid(ctx context.Context, token string, maxAttempts int) bool {
	var n int
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_challenges WHERE id = ? AND expires_at > ? AND attempts < ?`, sessionID(token), Now(), maxAttempts).Scan(&n)
	return n == 1
}

// DeleteChallenge ends a challenge (answered, or abandoned).
func (s *Store) DeleteChallenge(ctx context.Context, token string) {
	_, _ = s.db.ExecContext(ctx, `DELETE FROM login_challenges WHERE id = ? OR expires_at <= ?`, sessionID(token), Now())
}

// SessionRow is one signed-in browser, for the account page. ID is the stored hash, safe to show: it is not the token.
type SessionRow struct {
	ID, CreatedAt, ExpiresAt, UserAgent, IP string
	Current                                 bool
}

// Sessions lists a person's live sessions, newest first; current marks the one making the request.
func (s *Store) Sessions(ctx context.Context, userID int64, currentToken string) ([]SessionRow, error) {
	cur := sessionID(currentToken)
	rows, err := s.db.QueryContext(ctx, `SELECT id, created_at, expires_at, user_agent, ip FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC`, userID, Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var r SessionRow
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.ExpiresAt, &r.UserAgent, &r.IP); err != nil {
			return nil, err
		}
		r.Current = r.ID == cur
		out = append(out, r)
	}
	return out, rows.Err()
}

// EndSession signs out one of a person's sessions by its stored id.
func (s *Store) EndSession(ctx context.Context, userID int64, id string) error {
	return oneRow(s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id = ?`, userID, id))
}

// EndOtherSessions signs out every session of a person except the current one; returns how many.
func (s *Store) EndOtherSessions(ctx context.Context, userID int64, currentToken string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, sessionID(currentToken))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
