-- 0028: Git LFS (D82). Objects live on disk under data/lfs/<repo id>/ (content-addressed, checked on upload); these are
-- the limits a person may want to change.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('lfs.max_object_mb',     '2048', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('lfs.repo_quota_mb',     '0',    'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('lfs.batch_max',         '1000', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('lfs.ssh_token_minutes', '60',   'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
