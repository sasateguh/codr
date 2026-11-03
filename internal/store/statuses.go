package store

import "context"

// CommitStatus is one context's state on one commit (D81).
type CommitStatus struct {
	ID          int64
	SHA         string
	Context     string
	State       string // pending | success | failure | error
	Description string
	TargetURL   string
	Creator     string
	CreatedAt   string
	UpdatedAt   string
}

// SetStatus records the state of a context on a commit (a later call for the same context replaces it).
func (s *Store) SetStatus(ctx context.Context, repoID, creatorID int64, st CommitStatus) error {
	now := Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO commit_statuses (repo_id, sha, context, state, description, target_url, creator_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, sha, context) DO UPDATE SET state = excluded.state, description = excluded.description,
			target_url = excluded.target_url, creator_id = excluded.creator_id, updated_at = excluded.updated_at`,
		repoID, st.SHA, st.Context, st.State, st.Description, st.TargetURL, creatorID, now, now)
	return err
}

// Statuses lists the contexts of a commit, by name.
func (s *Store) Statuses(ctx context.Context, repoID int64, sha string) ([]CommitStatus, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.sha, c.context, c.state, c.description, c.target_url, COALESCE(u.name, ''), c.created_at, c.updated_at
		FROM commit_statuses c LEFT JOIN users u ON u.id = c.creator_id WHERE c.repo_id = ? AND c.sha = ? ORDER BY c.context`, repoID, sha)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommitStatus
	for rows.Next() {
		var c CommitStatus
		if err := rows.Scan(&c.ID, &c.SHA, &c.Context, &c.State, &c.Description, &c.TargetURL, &c.Creator, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountStatuses is how many contexts a commit has (a bound against a runaway runner).
func (s *Store) CountStatuses(ctx context.Context, repoID int64, sha string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commit_statuses WHERE repo_id = ? AND sha = ?`, repoID, sha).Scan(&n)
	return n, err
}

// ChecksGreen reports whether a commit carries at least one status and all of them are success (D89).
func (s *Store) ChecksGreen(ctx context.Context, repoID int64, sha string) (bool, error) {
	sts, err := s.Statuses(ctx, repoID, sha)
	if err != nil {
		return false, err
	}
	return Combined(sts) == "success", nil
}

// Combined is the overall state of a set of statuses: any failure/error wins, then pending, then success; "" = none.
func Combined(sts []CommitStatus) string {
	out := ""
	for _, s := range sts {
		switch s.State {
		case "failure", "error":
			return "failure"
		case "pending":
			out = "pending"
		case "success":
			if out == "" {
				out = "success"
			}
		}
	}
	return out
}
