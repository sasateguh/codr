-- 0026: two-step sign-in (D80). A TOTP secret per person, sealed under auth.secret_key; it counts only once confirmed
-- with a code (totp_on_at). The last accepted time step is kept so the same code cannot be used twice. Recovery codes
-- are stored as sha256 (they are random, so a slow hash adds nothing) and each works once. Between the password and
-- the code a sign-in is a short-lived challenge, never a session: until the code is right, nothing is signed in.
ALTER TABLE users ADD COLUMN totp_sealed TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN totp_on_at TEXT;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
CREATE TABLE recovery_codes (
  id         INTEGER PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT    NOT NULL UNIQUE,
  created_at TEXT    NOT NULL,
  used_at    TEXT
);
CREATE INDEX recovery_codes_user ON recovery_codes(user_id);
CREATE TABLE login_challenges (
  id         TEXT    PRIMARY KEY,            -- sha256 of the cookie token, never the token
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  next       TEXT    NOT NULL DEFAULT '/',
  ip         TEXT    NOT NULL DEFAULT '',
  attempts   INTEGER NOT NULL DEFAULT 0,
  created_at TEXT    NOT NULL,
  expires_at TEXT    NOT NULL
);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('auth.totp_skew_steps',     '1',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('auth.challenge_minutes',   '5',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('auth.challenge_attempts',  '5',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('auth.recovery_codes',      '10', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
