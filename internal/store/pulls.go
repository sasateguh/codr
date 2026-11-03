package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Pull is one pull request: head branch → base branch inside the same repo (FR-22, D53).
// Numbers share the repo's "#n" sequence with issues.
type Pull struct {
	ID          int64
	RepoID      int64
	Number      int64
	AuthorID    int64
	Author      string
	Title       string
	Body        string
	HeadBranch  string
	BaseBranch  string
	State       string // "open" | "closed" | "merged"
	MergeCommit string
	MergedBy    string
	CreatedAt   string
	UpdatedAt   string
	ClosedAt    string
	Comments    int
	Approvals   int    // standing approvals (latest verdict per reviewer)
	ExtURL      string // imported (D72): the original on GitHub; Author is then the GitHub login
	HeadRepoID  int64  // a pull request from a fork (D75): the repository holding HeadBranch; 0 = this repository
}

// Open reports whether the pull is still open.
func (p Pull) Open() bool { return p.State == "open" }

// Merged reports whether the pull was merged.
func (p Pull) Merged() bool { return p.State == "merged" }

// Review is one review row; the latest per reviewer counts.
type Review struct {
	ID         int64
	PullID     int64
	ReviewerID int64
	Reviewer   string
	Verdict    string // approve | changes | comment
	Body       string
	CreatedAt  string
}

// ReviewVerdicts are the accepted verdict values.
var ReviewVerdicts = []string{"approve", "changes", "comment"}

const pullCols = `p.id, p.repo_id, p.number, p.author_id, CASE WHEN p.ext_author <> '' THEN p.ext_author ELSE u.name END, p.title, p.body, p.head_branch, p.base_branch, p.state, COALESCE(p.merge_commit, ''), COALESCE(m.name, ''), p.created_at, p.updated_at, COALESCE(p.closed_at, ''),
	(SELECT COUNT(*) FROM pull_comments c WHERE c.pull_id = p.id),
	(SELECT COUNT(*) FROM pull_reviews r WHERE r.pull_id = p.id AND r.verdict = 'approve' AND r.id = (SELECT MAX(r2.id) FROM pull_reviews r2 WHERE r2.pull_id = p.id AND r2.reviewer_id = r.reviewer_id)), p.ext_url, COALESCE(p.head_repo_id, 0)`

const pullFrom = ` FROM pulls p JOIN users u ON u.id = p.author_id LEFT JOIN users m ON m.id = p.merged_by`

func scanPull(row interface{ Scan(...any) error }) (Pull, error) {
	var p Pull
	err := row.Scan(&p.ID, &p.RepoID, &p.Number, &p.AuthorID, &p.Author, &p.Title, &p.Body, &p.HeadBranch, &p.BaseBranch, &p.State, &p.MergeCommit, &p.MergedBy, &p.CreatedAt, &p.UpdatedAt, &p.ClosedAt, &p.Comments, &p.Approvals, &p.ExtURL, &p.HeadRepoID)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// CreatePull opens a pull request with the next shared "#n" of the repo.
func (s *Store) CreatePull(ctx context.Context, repoID, authorID int64, title, body, head, base string) (Pull, error) {
	return s.CreatePullFrom(ctx, repoID, authorID, title, body, 0, head, base)
}

// CreatePullFrom opens a pull request whose head branch lives in headRepoID (a fork; 0 = repoID itself).
func (s *Store) CreatePullFrom(ctx context.Context, repoID, authorID int64, title, body string, headRepoID int64, head, base string) (Pull, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Pull{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var n int64
	if err := tx.QueryRowContext(ctx, nextNumberSQL, repoID, repoID).Scan(&n); err != nil {
		return Pull{}, err
	}
	now := Now()
	var hr any
	if headRepoID > 0 && headRepoID != repoID {
		hr = headRepoID
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO pulls (repo_id, number, author_id, title, body, head_branch, base_branch, state, created_at, updated_at, head_repo_id) VALUES (?, ?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		repoID, n, authorID, title, body, head, base, now, now, hr)
	if err != nil {
		return Pull{}, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Pull{}, err
	}
	return s.PullByID(ctx, id)
}

// PullByID loads one pull request.
func (s *Store) PullByID(ctx context.Context, id int64) (Pull, error) {
	return scanPull(s.db.QueryRowContext(ctx, `SELECT `+pullCols+pullFrom+` WHERE p.id = ?`, id))
}

// PullByNumber loads repo pull #number.
func (s *Store) PullByNumber(ctx context.Context, repoID, number int64) (Pull, error) {
	return scanPull(s.db.QueryRowContext(ctx, `SELECT `+pullCols+pullFrom+` WHERE p.repo_id = ? AND p.number = ?`, repoID, number))
}

// OpenPullFor finds the open pull request from head into base, if any (one per branch pair).
func (s *Store) OpenPullFor(ctx context.Context, repoID int64, head, base string) (Pull, error) {
	return s.OpenPullFrom(ctx, repoID, 0, head, base)
}

// OpenPullFrom is OpenPullFor for a head branch in headRepoID (0 = the repository itself).
func (s *Store) OpenPullFrom(ctx context.Context, repoID, headRepoID int64, head, base string) (Pull, error) {
	if headRepoID == repoID {
		headRepoID = 0
	}
	return scanPull(s.db.QueryRowContext(ctx, `SELECT `+pullCols+pullFrom+` WHERE p.repo_id = ? AND COALESCE(p.head_repo_id, 0) = ? AND p.head_branch = ? AND p.base_branch = ? AND p.state = 'open'`, repoID, headRepoID, head, base))
}

// Pulls lists a repo's pull requests by state ("open" or "closed" = closed + merged), newest
// activity first, with an optional case-insensitive title filter and an assignee (≠0, D85).
func (s *Store) Pulls(ctx context.Context, repoID int64, state, q string, assigneeID int64, limit, offset int) ([]Pull, int, error) {
	where := `p.repo_id = ?`
	args := []any{repoID}
	if state == "open" {
		where += ` AND p.state = 'open'`
	} else {
		where += ` AND p.state <> 'open'`
	}
	if q = strings.TrimSpace(q); q != "" {
		where += ` AND lower(p.title) LIKE ?`
		args = append(args, "%"+strings.ToLower(q)+"%")
	}
	if assigneeID != 0 { // < 0 = an unknown name: nothing matches
		where += assignedFilter("p")
		args = append(args, assigneeID)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pulls p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+pullCols+pullFrom+` WHERE `+where+` ORDER BY p.updated_at DESC, p.number DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Pull
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

// PullCounts returns open and not-open pull requests of a repo.
func (s *Store) PullCounts(ctx context.Context, repoID int64) (open, closed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'open'), 0), COALESCE(SUM(state <> 'open'), 0) FROM pulls WHERE repo_id = ?`, repoID).Scan(&open, &closed)
	return
}

// SetPullState closes or reopens an open/closed pull; a merged pull cannot change state.
func (s *Store) SetPullState(ctx context.Context, id int64, closed bool) error {
	now := Now()
	var res sql.Result
	var err error
	if closed {
		res, err = s.db.ExecContext(ctx, `UPDATE pulls SET state = 'closed', closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`, now, now, id)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE pulls SET state = 'open', closed_at = NULL, updated_at = ? WHERE id = ? AND state = 'closed'`, now, id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPullMerged records a merge: state merged, the merge commit and who merged.
func (s *Store) SetPullMerged(ctx context.Context, id, byID int64, mergeCommit string) error {
	now := Now()
	res, err := s.db.ExecContext(ctx, `UPDATE pulls SET state = 'merged', merge_commit = ?, merged_by = ?, closed_at = ?, updated_at = ? WHERE id = ? AND state = 'open'`, mergeCommit, byID, now, now, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePull edits title and body.
func (s *Store) UpdatePull(ctx context.Context, id int64, title, body string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pulls SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddPullComment appends a comment and bumps the pull's activity time.
func (s *Store) AddPullComment(ctx context.Context, pullID, authorID int64, body string) (Comment, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO pull_comments (pull_id, author_id, body, created_at) VALUES (?, ?, ?, ?)`, pullID, authorID, body, now)
	if err != nil {
		return Comment{}, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `UPDATE pulls SET updated_at = ? WHERE id = ?`, now, pullID)
	var c Comment
	err = s.db.QueryRowContext(ctx, `SELECT c.id, c.pull_id, c.author_id, CASE WHEN c.ext_author <> '' THEN c.ext_author ELSE u.name END, c.body, c.created_at, c.ext_url FROM pull_comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?`, id).
		Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Author, &c.Body, &c.CreatedAt, &c.ExtURL)
	return c, err
}

// PullComments lists comments oldest first (IssueID carries the pull id).
func (s *Store) PullComments(ctx context.Context, pullID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.pull_id, c.author_id, CASE WHEN c.ext_author <> '' THEN c.ext_author ELSE u.name END, c.body, c.created_at, c.ext_url FROM pull_comments c JOIN users u ON u.id = c.author_id WHERE c.pull_id = ? ORDER BY c.id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Author, &c.Body, &c.CreatedAt, &c.ExtURL); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddReview records a verdict (approve / changes / comment) with an optional note.
func (s *Store) AddReview(ctx context.Context, pullID, reviewerID int64, verdict, body string) (Review, error) {
	ok := false
	for _, v := range ReviewVerdicts {
		ok = ok || v == verdict
	}
	if !ok {
		return Review{}, errors.New("store: bad verdict")
	}
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO pull_reviews (pull_id, reviewer_id, verdict, body, created_at) VALUES (?, ?, ?, ?, ?)`, pullID, reviewerID, verdict, body, now)
	if err != nil {
		return Review{}, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `UPDATE pulls SET updated_at = ? WHERE id = ?`, now, pullID)
	var r Review
	err = s.db.QueryRowContext(ctx, `SELECT r.id, r.pull_id, r.reviewer_id, u.name, r.verdict, r.body, r.created_at FROM pull_reviews r JOIN users u ON u.id = r.reviewer_id WHERE r.id = ?`, id).
		Scan(&r.ID, &r.PullID, &r.ReviewerID, &r.Reviewer, &r.Verdict, &r.Body, &r.CreatedAt)
	return r, err
}

// Reviews lists every review oldest first.
func (s *Store) Reviews(ctx context.Context, pullID int64) ([]Review, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.pull_id, r.reviewer_id, u.name, r.verdict, r.body, r.created_at FROM pull_reviews r JOIN users u ON u.id = r.reviewer_id WHERE r.pull_id = ? ORDER BY r.id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var r Review
		if err := rows.Scan(&r.ID, &r.PullID, &r.ReviewerID, &r.Reviewer, &r.Verdict, &r.Body, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Verdicts returns the standing verdict per reviewer (latest row wins), keyed by reviewer name.
func (s *Store) Verdicts(ctx context.Context, pullID int64) (map[string]string, error) {
	rs, err := s.Reviews(ctx, pullID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rs {
		if r.Verdict == "comment" {
			continue
		}
		out[r.Reviewer] = r.Verdict
	}
	return out, nil
}
