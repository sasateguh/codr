package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Issue is one row of issues joined with its author's name, plus its labels.
type Issue struct {
	ID        int64
	RepoID    int64
	Number    int64
	AuthorID  int64
	Author    string
	Title     string
	Body      string
	State     string // "open" | "closed"
	CreatedAt string
	UpdatedAt string
	ClosedAt  string
	Comments  int
	Labels    []Label
	ExtURL    string // imported (D72): the original on GitHub; Author is then the GitHub login
	ClosedBy  string // D87: the commit id, or "#n" of a pull request, whose "fixes #n" closed it; "" = closed by hand
}

// Closed reports whether the issue is closed.
func (i Issue) Closed() bool { return i.State == "closed" }

// Comment is one issue comment with its author's name.
type Comment struct {
	ID        int64
	IssueID   int64
	AuthorID  int64
	Author    string
	Body      string
	CreatedAt string
	ExtURL    string // imported (D72): the original on GitHub; Author is then the GitHub login
}

// Label is a per-repo label; Colour is a CSS tile class (h0–h7).
type Label struct {
	ID        int64
	RepoID    int64
	Name      string
	Colour    string
	CreatedAt string
	Open      int // open issues carrying it (filled by Labels)
}

// LabelColours are the only accepted colours (CSS .h0–.h7, D49).
var LabelColours = []string{"h0", "h1", "h2", "h3", "h4", "h5", "h6", "h7"}

// nextNumberSQL allocates the next "#n" of a repo: issues and pull requests share one sequence
// (D53) so a reference is never ambiguous. Run inside the insert transaction.
const nextNumberSQL = `SELECT COALESCE(MAX(n), 0) + 1 FROM (SELECT MAX(number) AS n FROM issues WHERE repo_id = ? UNION ALL SELECT MAX(number) FROM pulls WHERE repo_id = ?)`

// ErrExists is returned when a unique name is taken (labels).
var ErrExists = errors.New("already exists")

// ValidLabelColour reports whether c is one of LabelColours.
func ValidLabelColour(c string) bool {
	for _, x := range LabelColours {
		if x == c {
			return true
		}
	}
	return false
}

const issueCols = `i.id, i.repo_id, i.number, i.author_id, CASE WHEN i.ext_author <> '' THEN i.ext_author ELSE u.name END, i.title, i.body, i.state, i.created_at, i.updated_at, COALESCE(i.closed_at, ''),
	(SELECT COUNT(*) FROM issue_comments c WHERE c.issue_id = i.id), i.ext_url, i.closed_by_ref`

func scanIssue(row interface{ Scan(...any) error }) (Issue, error) {
	var i Issue
	err := row.Scan(&i.ID, &i.RepoID, &i.Number, &i.AuthorID, &i.Author, &i.Title, &i.Body, &i.State, &i.CreatedAt, &i.UpdatedAt, &i.ClosedAt, &i.Comments, &i.ExtURL, &i.ClosedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
}

// CreateIssue opens a new issue and allocates the next number in the repo.
func (s *Store) CreateIssue(ctx context.Context, repoID, authorID int64, title, body string) (Issue, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var n int64
	if err := tx.QueryRowContext(ctx, nextNumberSQL, repoID, repoID).Scan(&n); err != nil {
		return Issue{}, err
	}
	now := Now()
	res, err := tx.ExecContext(ctx, `INSERT INTO issues (repo_id, number, author_id, title, body, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'open', ?, ?)`,
		repoID, n, authorID, title, body, now, now)
	if err != nil {
		return Issue{}, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Issue{}, err
	}
	return s.IssueByID(ctx, id)
}

// IssueByID loads one issue with its labels.
func (s *Store) IssueByID(ctx context.Context, id int64) (Issue, error) {
	i, err := scanIssue(s.db.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues i JOIN users u ON u.id = i.author_id WHERE i.id = ?`, id))
	if err != nil {
		return i, err
	}
	i.Labels, err = s.issueLabels(ctx, i.ID)
	return i, err
}

// IssueByNumber loads repo issue #number with its labels.
func (s *Store) IssueByNumber(ctx context.Context, repoID, number int64) (Issue, error) {
	i, err := scanIssue(s.db.QueryRowContext(ctx, `SELECT `+issueCols+` FROM issues i JOIN users u ON u.id = i.author_id WHERE i.repo_id = ? AND i.number = ?`, repoID, number))
	if err != nil {
		return i, err
	}
	i.Labels, err = s.issueLabels(ctx, i.ID)
	return i, err
}

func (s *Store) issueLabels(ctx context.Context, issueID int64) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.repo_id, l.name, l.colour, l.created_at FROM issue_labels il JOIN labels l ON l.id = il.label_id WHERE il.issue_id = ? ORDER BY l.name`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.RepoID, &l.Name, &l.Colour, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Issues lists one page of a repo's issues in a state ("open"/"closed"), optionally only those
// carrying labelID (>0), assigned to assigneeID (≠0, D85) or whose title contains q (case-insensitive), newest activity first.
func (s *Store) Issues(ctx context.Context, repoID int64, state string, labelID int64, q string, assigneeID int64, limit, offset int) ([]Issue, int, error) {
	where := `WHERE i.repo_id = ? AND i.state = ?`
	args := []any{repoID, state}
	if labelID > 0 {
		where += ` AND EXISTS (SELECT 1 FROM issue_labels il WHERE il.issue_id = i.id AND il.label_id = ?)`
		args = append(args, labelID)
	}
	if q != "" {
		where += ` AND instr(lower(i.title), lower(?)) > 0`
		args = append(args, q)
	}
	if assigneeID != 0 { // < 0 = an unknown name: nothing matches
		where += assignedFilter("i")
		args = append(args, assigneeID)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM issues i `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+issueCols+` FROM issues i JOIN users u ON u.id = i.author_id `+where+` ORDER BY i.updated_at DESC, i.number DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	for k := range out {
		if out[k].Labels, err = s.issueLabels(ctx, out[k].ID); err != nil {
			return nil, 0, err
		}
	}
	return out, total, nil
}

// IssueCounts returns open and closed totals for a repo.
func (s *Store) IssueCounts(ctx context.Context, repoID int64) (open, closed int, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'open'), 0), COALESCE(SUM(state = 'closed'), 0) FROM issues WHERE repo_id = ?`, repoID).Scan(&open, &closed)
	return
}

// SetIssueState closes or reopens an issue.
func (s *Store) SetIssueState(ctx context.Context, id int64, closed bool) error {
	now := Now()
	var res sql.Result
	var err error
	if closed {
		res, err = s.db.ExecContext(ctx, `UPDATE issues SET state = 'closed', closed_at = ?, updated_at = ?, closed_by_ref = '' WHERE id = ?`, now, now, id)
	} else {
		res, err = s.db.ExecContext(ctx, `UPDATE issues SET state = 'open', closed_at = NULL, updated_at = ?, closed_by_ref = '' WHERE id = ?`, now, id)
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CloseIssueByRef closes an OPEN issue on behalf of a commit or pull request (D87); false = it was already closed.
func (s *Store) CloseIssueByRef(ctx context.Context, id int64, ref string) (bool, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `UPDATE issues SET state = 'closed', closed_at = ?, updated_at = ?, closed_by_ref = ? WHERE id = ? AND state = 'open'`, now, now, ref, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateIssue changes title and body.
func (s *Store) UpdateIssue(ctx context.Context, id int64, title, body string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE issues SET title = ?, body = ?, updated_at = ? WHERE id = ?`, title, body, Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddComment appends a comment and bumps the issue's activity time.
func (s *Store) AddComment(ctx context.Context, issueID, authorID int64, body string) (Comment, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO issue_comments (issue_id, author_id, body, created_at) VALUES (?, ?, ?, ?)`, issueID, authorID, body, now)
	if err != nil {
		return Comment{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, now, issueID); err != nil {
		return Comment{}, err
	}
	var c Comment
	err = s.db.QueryRowContext(ctx, `SELECT c.id, c.issue_id, c.author_id, CASE WHEN c.ext_author <> '' THEN c.ext_author ELSE u.name END, c.body, c.created_at, c.ext_url FROM issue_comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?`, id).
		Scan(&c.ID, &c.IssueID, &c.AuthorID, &c.Author, &c.Body, &c.CreatedAt, &c.ExtURL)
	return c, err
}

// Comments lists an issue's comments oldest first.
func (s *Store) Comments(ctx context.Context, issueID int64) ([]Comment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.issue_id, c.author_id, CASE WHEN c.ext_author <> '' THEN c.ext_author ELSE u.name END, c.body, c.created_at, c.ext_url FROM issue_comments c JOIN users u ON u.id = c.author_id WHERE c.issue_id = ? ORDER BY c.id`, issueID)
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

// CreateLabel adds a repo label; a duplicate name (case-insensitive) is ErrExists.
func (s *Store) CreateLabel(ctx context.Context, repoID int64, name, colour string) (Label, error) {
	name = strings.TrimSpace(name)
	var dup int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM labels WHERE repo_id = ? AND lower(name) = lower(?)`, repoID, name).Scan(&dup); err != nil {
		return Label{}, err
	}
	if dup > 0 {
		return Label{}, ErrExists
	}
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO labels (repo_id, name, colour, created_at) VALUES (?, ?, ?, ?)`, repoID, name, colour, now)
	if err != nil {
		return Label{}, err
	}
	id, _ := res.LastInsertId()
	return Label{ID: id, RepoID: repoID, Name: name, Colour: colour, CreatedAt: now}, nil
}

// DeleteLabel removes a label (and its issue links) when it belongs to the repo.
func (s *Store) DeleteLabel(ctx context.Context, repoID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM labels WHERE id = ? AND repo_id = ?`, id, repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Labels lists a repo's labels by name with their open-issue counts.
func (s *Store) Labels(ctx context.Context, repoID int64) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.repo_id, l.name, l.colour, l.created_at,
		(SELECT COUNT(*) FROM issue_labels il JOIN issues i ON i.id = il.issue_id WHERE il.label_id = l.id AND i.state = 'open')
		FROM labels l WHERE l.repo_id = ? ORDER BY l.name`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.RepoID, &l.Name, &l.Colour, &l.CreatedAt, &l.Open); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetIssueLabels replaces an issue's labels with the given ids (only labels of repoID are kept).
func (s *Store) SetIssueLabels(ctx context.Context, repoID, issueID int64, labelIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_labels WHERE issue_id = ?`, issueID); err != nil {
		return err
	}
	for _, id := range labelIDs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO issue_labels (issue_id, label_id) SELECT ?, id FROM labels WHERE id = ? AND repo_id = ?`, issueID, id, repoID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE issues SET updated_at = ? WHERE id = ?`, Now(), issueID); err != nil {
		return err
	}
	return tx.Commit()
}
