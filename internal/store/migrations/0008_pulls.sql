-- 0008: pull requests (v1.2, FR-22, D53). Numbers share the per-repo sequence with issues so that
-- "#n" is unambiguous; allocated inside the insert transaction (SQLite single writer).
CREATE TABLE pulls (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number       INTEGER NOT NULL,
  author_id    INTEGER NOT NULL REFERENCES users(id),
  title        TEXT    NOT NULL,
  body         TEXT    NOT NULL DEFAULT '',
  head_branch  TEXT    NOT NULL,
  base_branch  TEXT    NOT NULL,
  state        TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed','merged')),
  merge_commit TEXT,
  merged_by    INTEGER REFERENCES users(id),
  created_at   TEXT    NOT NULL,
  updated_at   TEXT    NOT NULL,
  closed_at    TEXT,
  UNIQUE (repo_id, number)
);
CREATE INDEX pulls_repo_state ON pulls(repo_id, state, updated_at);

CREATE TABLE pull_comments (
  id         INTEGER PRIMARY KEY,
  pull_id    INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  author_id  INTEGER NOT NULL REFERENCES users(id),
  body       TEXT    NOT NULL,
  created_at TEXT    NOT NULL
);
CREATE INDEX pull_comments_pull ON pull_comments(pull_id, id);

-- One row per review; the latest row per reviewer is the standing verdict.
CREATE TABLE pull_reviews (
  id          INTEGER PRIMARY KEY,
  pull_id     INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  reviewer_id INTEGER NOT NULL REFERENCES users(id),
  verdict     TEXT    NOT NULL CHECK (verdict IN ('approve','changes','comment')),
  body        TEXT    NOT NULL DEFAULT '',
  created_at  TEXT    NOT NULL
);
CREATE INDEX pull_reviews_pull ON pull_reviews(pull_id, id);

INSERT INTO settings (key, value, type, updated_at) VALUES
  ('pull.page_size',      '30',   'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('pull.require_review', 'false', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('pull.diff_max_lines', '3000', 'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now'));
