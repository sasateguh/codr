package store

import "context"

// LineComment is a comment on one line of a pull request diff (D65).
type LineComment struct {
	ID        int64
	PullID    int64
	AuthorID  int64
	Author    string
	Path      string
	Side      string // old | new
	Line      int
	Commit    string
	Body      string
	CreatedAt string
}

const lineCommentCols = `c.id, c.pull_id, c.author_id, u.name, c.path, c.side, c.line, c.commit_sha, c.body, c.created_at`

func scanLineComment(sc interface{ Scan(...any) error }) (LineComment, error) {
	var c LineComment
	err := sc.Scan(&c.ID, &c.PullID, &c.AuthorID, &c.Author, &c.Path, &c.Side, &c.Line, &c.Commit, &c.Body, &c.CreatedAt)
	return c, err
}

// AddLineComment stores a comment on path:line (side old|new) as seen at commit.
func (s *Store) AddLineComment(ctx context.Context, pullID, authorID int64, path, side string, line int, commit, body string) (LineComment, error) {
	now := Now()
	res, err := s.db.ExecContext(ctx, `INSERT INTO pull_line_comments (pull_id, author_id, path, side, line, commit_sha, body, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pullID, authorID, path, side, line, commit, body, now)
	if err != nil {
		return LineComment{}, err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `UPDATE pulls SET updated_at = ? WHERE id = ?`, now, pullID)
	return scanLineComment(s.db.QueryRowContext(ctx, `SELECT `+lineCommentCols+` FROM pull_line_comments c JOIN users u ON u.id = c.author_id WHERE c.id = ?`, id))
}

// LineComments lists a pull request's line comments, oldest first.
func (s *Store) LineComments(ctx context.Context, pullID int64) ([]LineComment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+lineCommentCols+` FROM pull_line_comments c JOIN users u ON u.id = c.author_id WHERE c.pull_id = ? ORDER BY c.id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineComment
	for rows.Next() {
		c, err := scanLineComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
