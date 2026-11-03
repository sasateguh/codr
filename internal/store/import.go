package store

import (
	"context"
	"database/sql"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
)

// Rows brought in from GitHub (D72). Every function is an upsert keyed on the GitHub id (ext_id), so running an
// import twice changes nothing and running it after new activity adds only what is new.

// ErrNumberTaken means a native Codr issue or pull already holds the GitHub number: the item is skipped, never
// renumbered (links to "#n" must keep meaning the same thing).
var ErrNumberTaken = errors.New("store: number already used by a Codr item")

// Imported is one GitHub issue or pull request as the store needs it.
type Imported struct {
	ExtID       int64
	Number      int64
	Title       string
	Body        string
	State       string // issues: open | closed; pulls: open | closed | merged
	Author      string // GitHub login
	URL         string // html_url
	CreatedAt   string
	UpdatedAt   string
	ClosedAt    string
	Head, Base  string // pulls only
	MergeCommit string // pulls only
	Labels      []string
}

// ImportedComment is one GitHub comment.
type ImportedComment struct {
	ExtID     int64
	Body      string
	Author    string
	URL       string
	CreatedAt string
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// numberFree reports whether number is unused in the repo, or used by the very row being imported.
func numberFree(ctx context.Context, tx *sql.Tx, repoID, number, extID int64) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM issues WHERE repo_id = ? AND number = ? AND (ext_id IS NULL OR ext_id <> ?)) +
		(SELECT COUNT(*) FROM pulls WHERE repo_id = ? AND number = ? AND (ext_id IS NULL OR ext_id <> ?))`, repoID, number, extID, repoID, number, extID).Scan(&n)
	return n == 0, err
}

// UpsertImportedIssue creates or updates a GitHub issue; by is the importing admin (author_id of record).
func (s *Store) UpsertImportedIssue(ctx context.Context, repoID, by int64, it Imported) (id int64, created bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	err = tx.QueryRowContext(ctx, `SELECT id FROM issues WHERE repo_id = ? AND ext_id = ?`, repoID, it.ExtID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if free, err := numberFree(ctx, tx, repoID, it.Number, it.ExtID); err != nil {
			return 0, false, err
		} else if !free {
			return 0, false, ErrNumberTaken
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO issues (repo_id, number, author_id, title, body, state, created_at, updated_at, closed_at, ext_author, ext_url, ext_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, it.Number, by, it.Title, it.Body, it.State, it.CreatedAt, it.UpdatedAt, nullable(it.ClosedAt), it.Author, it.URL, it.ExtID)
		if err != nil {
			return 0, false, err
		}
		id, _ = res.LastInsertId()
		created = true
	case err != nil:
		return 0, false, err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE issues SET title = ?, body = ?, state = ?, updated_at = ?, closed_at = ?, ext_author = ?, ext_url = ? WHERE id = ?`,
			it.Title, it.Body, it.State, it.UpdatedAt, nullable(it.ClosedAt), it.Author, it.URL, id); err != nil {
			return 0, false, err
		}
	}
	if err := setLabelsByName(ctx, tx, repoID, id, it.Labels); err != nil {
		return 0, false, err
	}
	return id, created, tx.Commit()
}

// UpsertImportedPull creates or updates a GitHub pull request as a record: it can be read, not merged here
// (the branches it names may no longer exist).
func (s *Store) UpsertImportedPull(ctx context.Context, repoID, by int64, it Imported) (id int64, created bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck
	err = tx.QueryRowContext(ctx, `SELECT id FROM pulls WHERE repo_id = ? AND ext_id = ?`, repoID, it.ExtID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if free, err := numberFree(ctx, tx, repoID, it.Number, it.ExtID); err != nil {
			return 0, false, err
		} else if !free {
			return 0, false, ErrNumberTaken
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO pulls (repo_id, number, author_id, title, body, head_branch, base_branch, state, merge_commit, created_at, updated_at, closed_at, ext_author, ext_url, ext_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, it.Number, by, it.Title, it.Body, it.Head, it.Base, it.State, nullable(it.MergeCommit), it.CreatedAt, it.UpdatedAt, nullable(it.ClosedAt), it.Author, it.URL, it.ExtID)
		if err != nil {
			return 0, false, err
		}
		id, _ = res.LastInsertId()
		created = true
	case err != nil:
		return 0, false, err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE pulls SET title = ?, body = ?, state = ?, merge_commit = ?, updated_at = ?, closed_at = ?, ext_author = ?, ext_url = ? WHERE id = ?`,
			it.Title, it.Body, it.State, nullable(it.MergeCommit), it.UpdatedAt, nullable(it.ClosedAt), it.Author, it.URL, id); err != nil {
			return 0, false, err
		}
	}
	return id, created, tx.Commit()
}

// AddImportedComment stores a comment under an issue (pull=false) or a pull request, once per GitHub id.
func (s *Store) AddImportedComment(ctx context.Context, pull bool, parentID, by int64, c ImportedComment) (bool, error) {
	table, col := "issue_comments", "issue_id"
	if pull {
		table, col = "pull_comments", "pull_id"
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE `+col+` = ? AND ext_id = ?`, parentID, c.ExtID).Scan(&id) // #nosec G202 -- table and column from the two fixed pairs above
	if err == nil {
		_, err = s.db.ExecContext(ctx, `UPDATE `+table+` SET body = ? WHERE id = ?`, c.Body, id) // #nosec G202 -- fixed table
		return false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+table+` (`+col+`, author_id, body, created_at, ext_author, ext_url, ext_id) VALUES (?, ?, ?, ?, ?, ?, ?)`, // #nosec G202 -- fixed table
		parentID, by, c.Body, c.CreatedAt, c.Author, c.URL, c.ExtID)
	return err == nil, err
}

// IssueIDByNumber and PullIDByNumber resolve "#n" for comments.
func (s *Store) IssueIDByNumber(ctx context.Context, repoID, number int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM issues WHERE repo_id = ? AND number = ?`, repoID, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// PullIDByNumber resolves a pull request number to its id.
func (s *Store) PullIDByNumber(ctx context.Context, repoID, number int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM pulls WHERE repo_id = ? AND number = ?`, repoID, number).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// labelColour picks one of the eight tile hues from the name, so a label keeps its colour across imports.
func labelColour(name string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return "h" + strconv.Itoa(int(h.Sum32()%8))
}

func setLabelsByName(ctx context.Context, tx *sql.Tx, repoID, issueID int64, names []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM issue_labels WHERE issue_id = ?`, issueID); err != nil {
		return err
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || len(n) > 50 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO labels (repo_id, name, colour, created_at) VALUES (?, ?, ?, ?) ON CONFLICT (repo_id, name) DO NOTHING`, repoID, n, labelColour(n), Now()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO issue_labels (issue_id, label_id) SELECT ?, id FROM labels WHERE repo_id = ? AND name = ?`, issueID, repoID, n); err != nil {
			return err
		}
	}
	return nil
}

// FirstAdmin is the oldest active admin: the account imported rows are recorded under (their display author
// stays the GitHub login).
func (s *Store) FirstAdmin(ctx context.Context) (User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE is_admin = 1 AND disabled_at IS NULL ORDER BY id LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}
