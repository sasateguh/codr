-- 0012: the file list shows each entry's last commit (D61). It is found with one bounded `git log` walk;
-- this is the bound (commits). Entries whose last change is older simply show no commit.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.tree_history_commits', '1000', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
