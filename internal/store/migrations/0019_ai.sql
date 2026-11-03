-- 0019: an AI assistant on pull requests, on the owner's terms (D71). Off until an admin sets an endpoint, turns it on
-- and opts repositories in one by one. The endpoint key is sealed under auth.secret_key; notes are stored with the
-- model, the head they read and how many bytes were sent, so nothing about a call is invisible later.
CREATE TABLE ai_config (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  enabled         INTEGER NOT NULL DEFAULT 0,
  endpoint        TEXT    NOT NULL DEFAULT '',
  model           TEXT    NOT NULL DEFAULT '',
  key_sealed      TEXT    NOT NULL DEFAULT '',
  max_diff_kb     INTEGER NOT NULL DEFAULT 96 CHECK (max_diff_kb BETWEEN 4 AND 2048),
  timeout_seconds INTEGER NOT NULL DEFAULT 80 CHECK (timeout_seconds BETWEEN 5 AND 600),
  updated_at      TEXT    NOT NULL
);
INSERT INTO ai_config (id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE TABLE ai_repos (
  repo_id    INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  enabled_by INTEGER,
  enabled_at TEXT    NOT NULL
);
CREATE TABLE pull_ai_notes (
  id           INTEGER PRIMARY KEY,
  pull_id      INTEGER NOT NULL REFERENCES pulls(id) ON DELETE CASCADE,
  kind         TEXT    NOT NULL CHECK (kind IN ('summary','review')),
  lang         TEXT    NOT NULL,
  model        TEXT    NOT NULL,
  head_sha     TEXT    NOT NULL,
  body         TEXT    NOT NULL,
  requested_by INTEGER NOT NULL REFERENCES users(id),
  bytes_sent   INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL
);
CREATE INDEX pull_ai_notes_pull ON pull_ai_notes(pull_id, id);
-- what the model is asked to do; editable at /admin/tetapan. The answer language is added by Codr.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ai.prompt_summary', 'You summarise a git pull request for its reviewers. Say in a few short paragraphs what changes and why it matters, then list the files that matter most. Be concrete; do not praise; do not invent anything that is not in the diff.', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ai.prompt_review',  'You review a git pull request. List concrete problems you can point to in the diff: bugs, missed edge cases, security issues, unclear names. For each, name the file and line and say why. If you find nothing serious, say so plainly. No praise, no generic advice.', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
