-- 0035: who made the product, for the landing page (D110): footer links, <meta name="author">, the schema.org
-- graph and llms.txt. Each person is shown only when both the name and an http(s) link are set; empty = not shown.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('landing.author_name',    '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.author_url',     '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.publisher_name', '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.publisher_url',  '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
