-- 0032: how pull requests may be merged, per repository (D88, PRD FR-22): a comma list of merge | squash | rebase, in the
-- order the merge box offers them (the first is preselected). Every repository starts with all three, like GitHub.
ALTER TABLE repos ADD COLUMN merge_styles TEXT NOT NULL DEFAULT 'merge,squash,rebase';
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('pull.rebase_max_commits', '250', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
