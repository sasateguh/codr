-- 0027: commit statuses (D81), the API a CI runner calls back on: a state per (commit, context), last write wins.
-- Shown on the commit page and on a pull request's head; sent to webhooks as the "status" event.
CREATE TABLE commit_statuses (
  id          INTEGER PRIMARY KEY,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  sha         TEXT    NOT NULL CHECK (length(sha) = 40),
  context     TEXT    NOT NULL,
  state       TEXT    NOT NULL CHECK (state IN ('pending','success','failure','error')),
  description TEXT    NOT NULL DEFAULT '',
  target_url  TEXT    NOT NULL DEFAULT '',
  creator_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at  TEXT    NOT NULL,
  updated_at  TEXT    NOT NULL,
  UNIQUE (repo_id, sha, context)
);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('api.per_page',          '30',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('api.per_page_max',      '100', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('api.statuses_per_sha',  '50',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
