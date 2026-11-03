-- 0009: the product landing page (v1.3, D56) on site.landing_hosts. Every value an owner may change
-- lives here, edited at /admin/tetapan. Empty forge_url = config http_host; empty source_url /
-- contact_email / demo_repo (owner/name, public) = that link is not shown; index=false = noindex (meta + X-Robots-Tag + robots.txt).
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('landing.forge_url',     '',      'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.demo_repo',     '',      'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.source_url',    '',      'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.contact_email', '',      'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('landing.index',         'false', 'bool',   strftime('%Y-%m-%dT%H:%M:%fZ','now'));
