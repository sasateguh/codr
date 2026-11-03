-- 0014: comments on one line of a pull request's diff (D65, FR-22). side = which file the line number
-- belongs to ('old' for a removed line, 'new' for an added or context line); commit_sha = the head the
-- reviewer was looking at, so a comment whose line moved on can be shown as outdated instead of misplaced.
CREATE TABLE pull_line_comments (
  id         INTEGER PRIMARY KEY,
  pull_id    INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  author_id  INTEGER NOT NULL REFERENCES users(id),
  path       TEXT    NOT NULL,
  side       TEXT    NOT NULL CHECK (side IN ('old','new')),
  line       INTEGER NOT NULL CHECK (line > 0),
  commit_sha TEXT    NOT NULL,
  body       TEXT    NOT NULL,
  created_at TEXT    NOT NULL
);
CREATE INDEX pull_line_comments_pull ON pull_line_comments(pull_id, id);
