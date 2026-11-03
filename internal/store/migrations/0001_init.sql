-- 0001_init: v0.1 schema. Migrations are append-only (NFR-9): never edit a shipped file.
CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  name          TEXT    NOT NULL UNIQUE,
  email         TEXT,
  password_hash TEXT    NOT NULL,
  is_admin      INTEGER NOT NULL DEFAULT 0,
  lang          TEXT    NOT NULL,
  theme         TEXT    NOT NULL DEFAULT 'auto',
  created_at    TEXT    NOT NULL,
  disabled_at   TEXT
);

CREATE TABLE ssh_keys (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  title        TEXT    NOT NULL,
  fingerprint  TEXT    NOT NULL UNIQUE,
  public_key   TEXT    NOT NULL,
  created_at   TEXT    NOT NULL,
  last_used_at TEXT
);
CREATE INDEX ssh_keys_user ON ssh_keys(user_id);

CREATE TABLE sessions (
  id         TEXT    PRIMARY KEY,            -- sha256 of the cookie token, never the token
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TEXT    NOT NULL,
  expires_at TEXT    NOT NULL,
  user_agent TEXT    NOT NULL DEFAULT '',
  ip         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE repos (
  id             INTEGER PRIMARY KEY,
  owner_id       INTEGER NOT NULL REFERENCES users(id),
  name           TEXT    NOT NULL,
  description    TEXT    NOT NULL DEFAULT '',
  is_private     INTEGER NOT NULL DEFAULT 1,
  default_branch TEXT    NOT NULL,
  archived_at    TEXT,
  created_at     TEXT    NOT NULL,
  updated_at     TEXT    NOT NULL,
  UNIQUE(owner_id, name)
);

CREATE TABLE repo_collaborators (
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL CHECK (role IN ('read','write','admin')),
  created_at TEXT    NOT NULL,
  PRIMARY KEY (repo_id, user_id)
);

-- Product settings (D14): the admin screen edits these; code reads them.
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  type       TEXT NOT NULL CHECK (type IN ('string','int','bool')),
  updated_at TEXT NOT NULL,
  updated_by INTEGER
);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('site.name',              'AMCD',  'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('site.default_language',  'ms-MY', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('site.registration_open', 'false', 'bool',   strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('repo.default_private',   'true',  'bool',   strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('repo.default_branch',    'main',  'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('repo.push_max_mb',       '500',   'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now'));

-- Append-only audit trail enforced by triggers, not discipline (PRD §8).
CREATE TABLE audit_log (
  id       INTEGER PRIMARY KEY,
  at       TEXT    NOT NULL,
  actor_id INTEGER,
  action   TEXT    NOT NULL,
  target   TEXT    NOT NULL,
  detail   TEXT    NOT NULL DEFAULT ''
);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
  BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
