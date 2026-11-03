-- 0003: v0.3 web UI settings (D14: numbers live here, not in code).
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.page_size',      '50',   'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.diff_max_lines', '2000', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.blob_max_kb',    '1024', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.log_page_size',  '40',   'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
