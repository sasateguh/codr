-- 0021: the statement printed on the data report (D73), edited at /admin/tetapan: who controls the data, where it
-- is kept, and whom to ask. Empty = the line says "not stated".
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('privacy.controller',    '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('privacy.data_location', '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('privacy.contact',       '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
