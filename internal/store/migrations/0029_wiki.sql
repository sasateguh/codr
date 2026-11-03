-- 0029: wiki (D83). Pages live in a git repository beside the code (data/repos/<id>.wiki.git); nothing in the database
-- but who may edit: write access, or, when this is on, anyone signed in on a public repository.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('wiki.public_edit', 'false', 'bool', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
