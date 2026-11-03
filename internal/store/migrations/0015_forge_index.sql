-- 0015: whether search engines may index the forge itself (D66). false = noindex on all three layers:
-- X-Robots-Tag on every response, <meta name="robots"> on every page, robots.txt "Disallow: /".
-- The landing page has its own switch (landing.index).
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('site.index', 'false', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
