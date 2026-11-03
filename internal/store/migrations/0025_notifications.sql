-- 0025: notifications in the app and by email (D79). A second reader of the event outbox (D78) turns issue and pull
-- request events into one row per person per thread: a thread that moves again is marked unread again instead of
-- piling up rows. Who hears about a thread: its author, everyone who commented or reviewed, people @mentioned, and
-- people watching the repository; never the person who acted, and never someone who cannot read the repository.
CREATE TABLE notifications (
  id          INTEGER PRIMARY KEY,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  thread      TEXT    NOT NULL CHECK (thread IN ('issue','pull')),
  number      INTEGER NOT NULL,
  title       TEXT    NOT NULL,
  reason      TEXT    NOT NULL,
  last_kind   TEXT    NOT NULL,
  last_action TEXT    NOT NULL DEFAULT '',
  actor_id    INTEGER REFERENCES users(id) ON DELETE SET NULL,
  event_id    INTEGER,
  updated_at  TEXT    NOT NULL,
  read_at     TEXT,
  emailed_at  TEXT,
  excerpt     TEXT    NOT NULL DEFAULT '',
  email_due   INTEGER NOT NULL DEFAULT 0,
  UNIQUE (user_id, repo_id, thread, number)
);
CREATE INDEX notifications_user ON notifications(user_id, read_at, updated_at);
CREATE INDEX notifications_mail ON notifications(email_due) WHERE email_due = 1;
-- watching: 'all' = every issue and pull request thread, 'ignore' = nothing, even when mentioned. No row = the default:
-- the owner of a personal repository watches it, everyone else hears only about threads they take part in.
CREATE TABLE repo_watch (
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  level      TEXT    NOT NULL CHECK (level IN ('all','participating','ignore')),
  updated_at TEXT    NOT NULL,
  PRIMARY KEY (user_id, repo_id)
);
-- per person: email on or off (on by default; nothing is sent without an address and a configured server)
ALTER TABLE users ADD COLUMN email_notify INTEGER NOT NULL DEFAULT 1;
-- the outgoing mail server: one row, password sealed under auth.secret_key like every other secret
CREATE TABLE mail_config (
  id              INTEGER PRIMARY KEY CHECK (id = 1),
  enabled         INTEGER NOT NULL DEFAULT 0,
  host            TEXT    NOT NULL DEFAULT '',
  port            INTEGER NOT NULL DEFAULT 465 CHECK (port BETWEEN 1 AND 65535),
  security        TEXT    NOT NULL DEFAULT 'tls' CHECK (security IN ('tls','starttls')),
  username        TEXT    NOT NULL DEFAULT '',
  password_sealed TEXT    NOT NULL DEFAULT '',
  from_addr       TEXT    NOT NULL DEFAULT '',
  last_ok_at      TEXT,
  last_error      TEXT    NOT NULL DEFAULT '',
  updated_at      TEXT    NOT NULL
);
INSERT INTO mail_config (id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
-- the notification reader starts at the newest event: history from before this release is not announced
INSERT INTO event_cursors (name, last_id) VALUES ('notify', (SELECT COALESCE(MAX(id), 0) FROM events));
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('notify.page_size',             '50',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('notify.keep_days',             '90',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mail.thread_cooldown_minutes', '15',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mail.timeout_seconds',         '20',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('mail.per_tick',                '20',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
