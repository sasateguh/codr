-- 0004: v0.4 — mirrors (import from URL, scheduled refresh). Token is AES-GCM under config auth.secret_key.
CREATE TABLE repo_mirrors (
  repo_id          INTEGER PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
  url              TEXT    NOT NULL,
  token_enc        TEXT    NOT NULL DEFAULT '',
  interval_minutes INTEGER NOT NULL,
  last_sync_at     TEXT,
  last_ok_at       TEXT,
  last_error       TEXT    NOT NULL DEFAULT '',
  created_at       TEXT    NOT NULL
);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('mirror.enabled',                  'true', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mirror.default_interval_minutes', '60',   'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('doctor.min_free_mb',              '2048', 'int',  strftime('%Y-%m-%dT%H:%M:%fZ','now'));
