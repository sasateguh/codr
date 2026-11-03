package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Mirror is a row of repo_mirrors.
type Mirror struct {
	RepoID          int64
	URL             string
	TokenEnc        string
	IntervalMinutes int64
	LastSyncAt      string
	LastOKAt        string
	LastError       string
	CreatedAt       string
}

const mirrorCols = `repo_id, url, token_enc, interval_minutes, COALESCE(last_sync_at,''), COALESCE(last_ok_at,''), last_error, created_at`

func scanMirror(row interface{ Scan(...any) error }) (Mirror, error) {
	var m Mirror
	err := row.Scan(&m.RepoID, &m.URL, &m.TokenEnc, &m.IntervalMinutes, &m.LastSyncAt, &m.LastOKAt, &m.LastError, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Mirror{}, ErrNotFound
	}
	return m, err
}

// SetMirror marks a repo as a mirror of url (token already sealed; "" = none).
func (s *Store) SetMirror(ctx context.Context, repoID int64, url, tokenEnc string, intervalMinutes int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_mirrors (repo_id, url, token_enc, interval_minutes, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(repo_id) DO UPDATE SET url = excluded.url, token_enc = excluded.token_enc, interval_minutes = excluded.interval_minutes`,
		repoID, url, tokenEnc, intervalMinutes, Now())
	return err
}

// MirrorOf returns the mirror row for a repo.
func (s *Store) MirrorOf(ctx context.Context, repoID int64) (Mirror, error) {
	return scanMirror(s.db.QueryRowContext(ctx, `SELECT `+mirrorCols+` FROM repo_mirrors WHERE repo_id = ?`, repoID))
}

// RemoveMirror turns a mirror back into an ordinary repo.
func (s *Store) RemoveMirror(ctx context.Context, repoID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM repo_mirrors WHERE repo_id = ?`, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Mirrors lists every mirror (for the scheduler and doctor).
func (s *Store) Mirrors(ctx context.Context) ([]Mirror, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mirrorCols+` FROM repo_mirrors ORDER BY repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mirror
	for rows.Next() {
		m, err := scanMirror(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MirrorsDue lists mirrors whose last sync is older than their interval (or never ran).
func (s *Store) MirrorsDue(ctx context.Context) ([]Mirror, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mirrorCols+` FROM repo_mirrors
		WHERE last_sync_at IS NULL OR datetime(last_sync_at, '+' || interval_minutes || ' minutes') <= datetime(?) ORDER BY last_sync_at`, Now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Mirror
	for rows.Next() {
		m, err := scanMirror(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MirrorSynced records the outcome of one sync attempt.
func (s *Store) MirrorSynced(ctx context.Context, repoID int64, syncErr string) error {
	now := Now()
	if syncErr == "" {
		_, err := s.db.ExecContext(ctx, `UPDATE repo_mirrors SET last_sync_at = ?, last_ok_at = ?, last_error = '' WHERE repo_id = ?`, now, now, repoID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE repo_mirrors SET last_sync_at = ?, last_error = ? WHERE repo_id = ?`, now, syncErr, repoID)
	return err
}

// AuditRow is one audit_log entry joined with the actor's name.
type AuditRow struct {
	ID     int64
	At     string
	Actor  string
	Action string
	Target string
	Detail string
}

// AuditRows pages the audit trail, newest first.
func (s *Store) AuditRows(ctx context.Context, limit, offset int) ([]AuditRow, int, error) {
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.at, COALESCE(u.name, ''), a.action, a.target, a.detail FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id ORDER BY a.id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.Target, &r.Detail); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// ActivityOf returns one actor's audit rows of the given actions since a timestamp, newest first (D62).
func (s *Store) ActivityOf(ctx context.Context, actorID int64, since string, actions []string, limit int) ([]AuditRow, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	args := []any{actorID, since}
	for _, a := range actions {
		args = append(args, a)
	}
	args = append(args, limit)
	q := `SELECT id, at, '', action, target, detail FROM audit_log WHERE actor_id = ? AND at >= ? AND action IN (?` +
		strings.Repeat(", ?", len(actions)-1) + `) ORDER BY id DESC LIMIT ?` // #nosec G202 -- only placeholders are joined
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.Target, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActivityRecent returns the newest audit rows of the given actions by anyone, with the actor's name (D68).
// The caller filters by what the viewer may read; limit bounds the rows read, not the rows shown.
func (s *Store) ActivityRecent(ctx context.Context, actions []string, limit int) ([]AuditRow, error) {
	if len(actions) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(actions)+1)
	for _, a := range actions {
		args = append(args, a)
	}
	args = append(args, limit)
	q := `SELECT a.id, a.at, COALESCE(u.name, ''), a.action, a.target, a.detail FROM audit_log a LEFT JOIN users u ON u.id = a.actor_id WHERE a.action IN (?` +
		strings.Repeat(", ?", len(actions)-1) + `) ORDER BY a.id DESC LIMIT ?` // #nosec G202 -- only placeholders are joined
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.Target, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AuditOf pages one actor's own audit rows, newest first (D86, FR-25), optionally only actions starting "area.".
func (s *Store) AuditOf(ctx context.Context, actorID int64, area string, limit, offset int) ([]AuditRow, int, error) {
	where, args := `actor_id = ?`, []any{actorID}
	if area != "" {
		where += ` AND substr(action, 1, ?) = ?`
		args = append(args, len(area)+1, area+".")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, '', action, target, detail FROM audit_log WHERE `+where+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AuditRow
	for rows.Next() {
		var r AuditRow
		if err := rows.Scan(&r.ID, &r.At, &r.Actor, &r.Action, &r.Target, &r.Detail); err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// AuditPrefixes lists the distinct action prefixes ("issue", "git", …) one actor has rows for (D86).
func (s *Store) AuditPrefixes(ctx context.Context, actorID int64) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT CASE WHEN instr(action, '.') > 0 THEN substr(action, 1, instr(action, '.') - 1) ELSE action END FROM audit_log WHERE actor_id = ?`, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out[p] = true
	}
	return out, rows.Err()
}
