package store

import (
	"context"
	"database/sql"
	"errors"
)

// AIConfig is the single AI assistant configuration row (D71). KeySealed is sealed under auth.secret_key.
type AIConfig struct {
	Enabled        bool
	Endpoint       string
	Model          string
	KeySealed      string
	MaxDiffKB      int
	TimeoutSeconds int
	UpdatedAt      string
}

// AINote is one stored answer of the assistant on a pull request.
type AINote struct {
	ID          int64
	PullID      int64
	Kind        string // summary | review
	Lang        string
	Model       string
	HeadSHA     string
	Body        string
	RequestedBy string
	BytesSent   int
	CreatedAt   string
}

// AIConfig reads the configuration row.
func (s *Store) AIConfig(ctx context.Context) (AIConfig, error) {
	var c AIConfig
	var en int
	err := s.db.QueryRowContext(ctx, `SELECT enabled, endpoint, model, key_sealed, max_diff_kb, timeout_seconds, updated_at FROM ai_config WHERE id = 1`).
		Scan(&en, &c.Endpoint, &c.Model, &c.KeySealed, &c.MaxDiffKB, &c.TimeoutSeconds, &c.UpdatedAt)
	c.Enabled = en == 1
	return c, err
}

// SetAIConfig replaces the configuration row.
func (s *Store) SetAIConfig(ctx context.Context, c AIConfig) error {
	en := 0
	if c.Enabled {
		en = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ai_config SET enabled = ?, endpoint = ?, model = ?, key_sealed = ?, max_diff_kb = ?, timeout_seconds = ?, updated_at = ? WHERE id = 1`,
		en, c.Endpoint, c.Model, c.KeySealed, c.MaxDiffKB, c.TimeoutSeconds, Now())
	return err
}

// AIRepoEnabled reports whether a repository opted in.
func (s *Store) AIRepoEnabled(ctx context.Context, repoID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM ai_repos WHERE repo_id = ?`, repoID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AIRepos lists the opted-in repository ids.
func (s *Store) AIRepos(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id FROM ai_repos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SetAIRepos replaces the opted-in set in one transaction.
func (s *Store) SetAIRepos(ctx context.Context, ids []int64, by int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM ai_repos`); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ai_repos (repo_id, enabled_by, enabled_at) VALUES (?, ?, ?)`, id, by, Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// AddAINote stores an answer.
func (s *Store) AddAINote(ctx context.Context, n AINote, by int64) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO pull_ai_notes (pull_id, kind, lang, model, head_sha, body, requested_by, bytes_sent, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.PullID, n.Kind, n.Lang, n.Model, n.HeadSHA, n.Body, by, n.BytesSent, Now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// AINotes lists a pull request's notes, newest first.
func (s *Store) AINotes(ctx context.Context, pullID int64) ([]AINote, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT n.id, n.pull_id, n.kind, n.lang, n.model, n.head_sha, n.body, u.name, n.bytes_sent, n.created_at FROM pull_ai_notes n JOIN users u ON u.id = n.requested_by WHERE n.pull_id = ? ORDER BY n.id DESC`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AINote
	for rows.Next() {
		var n AINote
		if err := rows.Scan(&n.ID, &n.PullID, &n.Kind, &n.Lang, &n.Model, &n.HeadSHA, &n.Body, &n.RequestedBy, &n.BytesSent, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
