-- 0034: sign-in through an OpenID Connect provider (D91, Fasa 3c). Off by default. One provider (a company's IdP);
-- the client secret is sealed under auth.secret_key like every other secret. A person signs in with the provider only
-- when the provider's VERIFIED email equals the email of an existing Codr account (first time) or when their provider
-- subject is already linked to an account; 2FA is still asked afterwards. No account is created by the provider.
CREATE TABLE oidc_config (
  id            INTEGER PRIMARY KEY CHECK (id = 1),
  enabled       INTEGER NOT NULL DEFAULT 0,
  name          TEXT    NOT NULL DEFAULT '',
  issuer        TEXT    NOT NULL DEFAULT '',
  client_id     TEXT    NOT NULL DEFAULT '',
  secret_sealed TEXT    NOT NULL DEFAULT '',
  scopes        TEXT    NOT NULL DEFAULT 'openid email profile',
  updated_at    TEXT    NOT NULL
);
INSERT INTO oidc_config (id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ','now'));
-- one row per sign-in in progress: the state the provider returns, the hash of a cookie that binds it to the browser
-- that started it, the nonce the ID token must carry and the PKCE verifier. Used once, then deleted.
CREATE TABLE oidc_flows (
  state        TEXT PRIMARY KEY,
  browser_hash TEXT NOT NULL,
  nonce        TEXT NOT NULL,
  verifier     TEXT NOT NULL,
  next         TEXT NOT NULL DEFAULT '/',
  created_at   TEXT NOT NULL
);
CREATE TABLE oidc_links (
  issuer       TEXT    NOT NULL,
  subject      TEXT    NOT NULL,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  email        TEXT    NOT NULL DEFAULT '',
  created_at   TEXT    NOT NULL,
  last_used_at TEXT,
  PRIMARY KEY (issuer, subject)
);
CREATE INDEX oidc_links_user ON oidc_links(user_id);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('auth.oidc_flow_minutes', '10', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('auth.oidc_timeout_seconds', '15', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
