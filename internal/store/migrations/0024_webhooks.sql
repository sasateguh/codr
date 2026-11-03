-- 0024: outgoing webhooks signed with HMAC (D78). Every repository event that someone else may want to hear about is
-- written once to `events` (an outbox, beside the audit row); a dispatcher in `serve` reads it with a cursor that
-- survives restarts, fans each event out to the repository's webhooks and records every attempt. Notifications read
-- the same outbox later, so there is one place where "something happened" is written.
CREATE TABLE events (
  id       INTEGER PRIMARY KEY,
  at       TEXT    NOT NULL,
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  actor_id INTEGER,
  kind     TEXT    NOT NULL,
  data     TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX events_repo ON events(repo_id, id);
-- how far each reader of the outbox has got (webhook dispatcher today, notifications next)
CREATE TABLE event_cursors (
  name    TEXT    PRIMARY KEY,
  last_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE webhooks (
  id            INTEGER PRIMARY KEY,
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  url           TEXT    NOT NULL,
  secret_sealed TEXT    NOT NULL,
  events        TEXT    NOT NULL DEFAULT '*',
  active        INTEGER NOT NULL DEFAULT 1,
  since_event   INTEGER NOT NULL DEFAULT 0,
  created_by    INTEGER,
  created_at    TEXT    NOT NULL,
  updated_at    TEXT    NOT NULL
);
CREATE INDEX webhooks_repo ON webhooks(repo_id);
CREATE TABLE webhook_deliveries (
  id           INTEGER PRIMARY KEY,
  webhook_id   INTEGER NOT NULL REFERENCES webhooks(id) ON DELETE CASCADE,
  event_id     INTEGER REFERENCES events(id) ON DELETE SET NULL,
  kind         TEXT    NOT NULL,
  guid         TEXT    NOT NULL,
  state        TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','ok','failed')),
  attempts     INTEGER NOT NULL DEFAULT 0,
  status_code  INTEGER NOT NULL DEFAULT 0,
  error        TEXT    NOT NULL DEFAULT '',
  duration_ms  INTEGER NOT NULL DEFAULT 0,
  response     TEXT    NOT NULL DEFAULT '',
  payload      TEXT    NOT NULL DEFAULT '',
  created_at   TEXT    NOT NULL,
  next_at      TEXT    NOT NULL,
  done_at      TEXT
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries(state, next_at);
CREATE INDEX webhook_deliveries_hook ON webhook_deliveries(webhook_id, id);
-- the outbox starts empty, so the dispatcher starts at 0; a webhook only hears events after it was made (since_event)
INSERT INTO event_cursors (name, last_id) VALUES ('webhook', 0);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('webhook.timeout_seconds', '10',  'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.max_attempts',    '5',   'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.retry_seconds',   '60',  'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.per_repo',        '20',  'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.push_commits',    '20',  'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.keep_days',       '30',  'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.allow_private',   'false', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('webhook.allow_hosts',     '',    'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
