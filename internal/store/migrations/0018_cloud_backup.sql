-- 0018: backups to the owner's own S3-compatible bucket (D70). One configuration row; the bucket secret and
-- the recovery key are sealed under auth.secret_key (never plain in the DB, never in the generic settings
-- page). Each run is one row, so the admin page and `doctor` read what really happened.
CREATE TABLE cloud_backup (
  id             INTEGER PRIMARY KEY CHECK (id = 1),
  enabled        INTEGER NOT NULL DEFAULT 0,
  endpoint       TEXT    NOT NULL DEFAULT '',
  region         TEXT    NOT NULL DEFAULT 'auto',
  bucket         TEXT    NOT NULL DEFAULT '',
  prefix         TEXT    NOT NULL DEFAULT 'codr',
  access_key     TEXT    NOT NULL DEFAULT '',
  secret_sealed  TEXT    NOT NULL DEFAULT '',
  key_sealed     TEXT    NOT NULL DEFAULT '',
  key_hint       TEXT    NOT NULL DEFAULT '',
  keep           INTEGER NOT NULL DEFAULT 14 CHECK (keep BETWEEN 1 AND 1000),
  interval_hours INTEGER NOT NULL DEFAULT 24 CHECK (interval_hours BETWEEN 1 AND 720),
  updated_at     TEXT    NOT NULL
);
INSERT INTO cloud_backup (id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
CREATE TABLE cloud_backup_runs (
  id          INTEGER PRIMARY KEY,
  started_at  TEXT    NOT NULL,
  finished_at TEXT    NOT NULL DEFAULT '',
  status      TEXT    NOT NULL CHECK (status IN ('running','ok','failed')),
  object      TEXT    NOT NULL DEFAULT '',
  bytes       INTEGER NOT NULL DEFAULT 0,
  repos       INTEGER NOT NULL DEFAULT 0,
  detail      TEXT    NOT NULL DEFAULT '',
  trigger     TEXT    NOT NULL DEFAULT ''
);
