-- 0006: in-repo search (v0.6.3) limits live in settings (D14).
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.search_max_hits',  '200', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.search_max_files', '50',  'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
