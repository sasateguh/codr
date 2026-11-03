package store

import (
	"context"
	"strings"
)

// Assignees (D85, FR-21): who carries an issue or a pull request. "#n" is shared by both (D53), so rows are keyed by
// (repo, number). Who may be assigned and who may assign is the web layer's decision; these functions only store it.

// Assignee is one assigned person.
type Assignee struct {
	UserID int64
	Name   string
}

// Assignees lists the people assigned to repo #number, by name.
func (s *Store) Assignees(ctx context.Context, repoID, number int64) ([]Assignee, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.user_id, u.name FROM assignees a JOIN users u ON u.id = a.user_id
		WHERE a.repo_id = ? AND a.number = ? ORDER BY lower(u.name)`, repoID, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignee
	for rows.Next() {
		var a Assignee
		if err := rows.Scan(&a.UserID, &a.Name); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssigneeNames maps each listed number of a repo to its assignees' names (one query for a list page).
func (s *Store) AssigneeNames(ctx context.Context, repoID int64, numbers []int64) (map[int64][]string, error) {
	out := map[int64][]string{}
	if len(numbers) == 0 {
		return out, nil
	}
	args := []any{repoID}
	for _, n := range numbers {
		args = append(args, n)
	}
	q := `SELECT a.number, u.name FROM assignees a JOIN users u ON u.id = a.user_id WHERE a.repo_id = ? AND a.number IN (?` +
		strings.Repeat(", ?", len(numbers)-1) + `) ORDER BY lower(u.name)` // #nosec G202 -- only placeholders are joined
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n int64
		var name string
		if err := rows.Scan(&n, &name); err != nil {
			return nil, err
		}
		out[n] = append(out[n], name)
	}
	return out, rows.Err()
}

// Assign adds userID to repo #number; false = already assigned. The thread's activity time moves.
func (s *Store) Assign(ctx context.Context, repoID, number, userID, byID int64) (bool, error) {
	var by any
	if byID > 0 {
		by = byID
	}
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO assignees (repo_id, number, user_id, assigned_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		repoID, number, userID, by, Now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.touchThread(ctx, repoID, number)
	}
	return n > 0, nil
}

// Unassign removes userID from repo #number; false = was not assigned.
func (s *Store) Unassign(ctx context.Context, repoID, number, userID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM assignees WHERE repo_id = ? AND number = ? AND user_id = ?`, repoID, number, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		s.touchThread(ctx, repoID, number)
	}
	return n > 0, nil
}

// AssigneeCount is how many people carry repo #number.
func (s *Store) AssigneeCount(ctx context.Context, repoID, number int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignees WHERE repo_id = ? AND number = ?`, repoID, number).Scan(&n)
	return n, err
}

func (s *Store) touchThread(ctx context.Context, repoID, number int64) {
	now := Now()
	_, _ = s.db.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE repo_id = ? AND number = ?`, now, repoID, number)
	_, _ = s.db.ExecContext(ctx, `UPDATE pulls SET updated_at = ? WHERE repo_id = ? AND number = ?`, now, repoID, number)
}

// assignedFilter is the WHERE fragment that keeps threads assigned to a user; alias is the issues/pulls table alias.
func assignedFilter(alias string) string {
	return ` AND EXISTS (SELECT 1 FROM assignees a WHERE a.repo_id = ` + alias + `.repo_id AND a.number = ` + alias + `.number AND a.user_id = ?)`
}
