-- 0022: organisations and teams (D74). An organisation is a namespace like a user (one name space, repos owned
-- the same way) that can never sign in: no password ('!' never verifies), no keys, no tokens. Members are owners
-- (manage everything) or members (the organisation's base role on every repo); teams grant a role on chosen repos.
ALTER TABLE users ADD COLUMN kind          TEXT NOT NULL DEFAULT 'user' CHECK (kind IN ('user','org'));
ALTER TABLE users ADD COLUMN org_base_role TEXT NOT NULL DEFAULT 'read' CHECK (org_base_role IN ('none','read','write'));
ALTER TABLE users ADD COLUMN display_name  TEXT NOT NULL DEFAULT '';
CREATE TABLE org_members (
  org_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT    NOT NULL CHECK (role IN ('owner','member')),
  created_at TEXT    NOT NULL,
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX org_members_user ON org_members(user_id);
CREATE TABLE teams (
  id         INTEGER PRIMARY KEY,
  org_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name       TEXT    NOT NULL,
  created_at TEXT    NOT NULL,
  UNIQUE (org_id, name)
);
CREATE TABLE team_members (
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  PRIMARY KEY (team_id, user_id)
);
CREATE INDEX team_members_user ON team_members(user_id);
CREATE TABLE team_repos (
  team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  role    TEXT    NOT NULL CHECK (role IN ('read','write','admin')),
  PRIMARY KEY (team_id, repo_id)
);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('org.allow_create', 'true', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
