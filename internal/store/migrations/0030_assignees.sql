-- 0030: assignees on issues and pull requests (D85, PRD FR-21). "#n" is shared by issues and pull requests (D53),
-- so one table keyed by (repo, number) serves both. Who may be assigned is decided in the web layer (assignCan);
-- the row only records who carries the thread and who put them there.
CREATE TABLE assignees (
  repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number      INTEGER NOT NULL,
  user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  assigned_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at  TEXT    NOT NULL,
  PRIMARY KEY (repo_id, number, user_id)
);
CREATE INDEX assignees_user ON assignees(user_id, repo_id);
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('issue.max_assignees',        '10', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('account.activity_page_size', '50', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
