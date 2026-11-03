-- 0007: issues (v1.1, FR-21, D50). Numbers are per repo (#1, #2, …), allocated inside the insert
-- transaction; SQLite is single-writer here, so MAX(number)+1 is race-free.
CREATE TABLE issues (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number     INTEGER NOT NULL,
  author_id  INTEGER NOT NULL REFERENCES users(id),
  title      TEXT    NOT NULL,
  body       TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
  created_at TEXT    NOT NULL,
  updated_at TEXT    NOT NULL,
  closed_at  TEXT,
  UNIQUE (repo_id, number)
);
CREATE INDEX issues_repo_state ON issues(repo_id, state, updated_at);

CREATE TABLE issue_comments (
  id         INTEGER PRIMARY KEY,
  issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  author_id  INTEGER NOT NULL REFERENCES users(id),
  body       TEXT    NOT NULL,
  created_at TEXT    NOT NULL
);
CREATE INDEX issue_comments_issue ON issue_comments(issue_id, id);

-- Labels are per repo; the colour is one of the eight tile hues in CSS (.h0–.h7), never a free hex.
CREATE TABLE labels (
  id         INTEGER PRIMARY KEY,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  colour     TEXT    NOT NULL CHECK (colour IN ('h0','h1','h2','h3','h4','h5','h6','h7')),
  created_at TEXT    NOT NULL,
  UNIQUE (repo_id, name)
);

CREATE TABLE issue_labels (
  issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
  label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  PRIMARY KEY (issue_id, label_id)
);

INSERT INTO settings (key, value, type, updated_at) VALUES
  ('issue.page_size',     '30',   'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('issue.body_max_kb',   '64',   'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('issue.public_create', 'true', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
