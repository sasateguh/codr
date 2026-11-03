-- 0002: v0.2 — access tokens (HTTPS git, D8/D28), protected branches, new settings.
CREATE TABLE access_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         TEXT    NOT NULL,
  token_hash   TEXT    NOT NULL UNIQUE,   -- sha256 of the token, never the token
  scopes       TEXT    NOT NULL,          -- comma list: repo:read, repo:write, admin
  created_at   TEXT    NOT NULL,
  expires_at   TEXT    NOT NULL,
  last_used_at TEXT
);
CREATE INDEX access_tokens_user ON access_tokens(user_id);

-- A protected branch refuses deletion and non-fast-forward pushes unless allowed here.
-- pattern is a glob on the branch name (path.Match), e.g. main, release/*.
CREATE TABLE protected_branches (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  pattern      TEXT    NOT NULL,
  allow_force  INTEGER NOT NULL DEFAULT 0,
  allow_delete INTEGER NOT NULL DEFAULT 0,
  created_at   TEXT    NOT NULL,
  PRIMARY KEY (repo_id, pattern)
);

INSERT INTO settings (key, value, type, updated_at) VALUES
  ('repo.protect_default_branch', 'true', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('repo.receive_fsck',           'true', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('token.default_days',          '90',   'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('token.max_days',              '365',  'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now'));
