-- 0017: the "What comes next" section of the landing page (D69). Order and status of each item, edited at
-- /admin/tetapan without touching code: key=status, comma separated; status live | soon | planned.
-- Text for each key lives in locale/*.json (landing.next.<key>.t / .d); an unknown key is ignored.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('landing.roadmap', 'cloud=soon,ai=soon,import=soon,pdpa=soon,orgs=planned', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
