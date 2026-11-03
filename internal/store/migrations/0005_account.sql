-- 0005: the account page (v0.6) shows an operator-written SSH connection note — e.g. the
-- cloudflared ProxyCommand a tunnel needs. Empty = nothing shown. Edited at /admin/tetapan.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.ssh_hint', '', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
