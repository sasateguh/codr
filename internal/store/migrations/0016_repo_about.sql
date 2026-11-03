-- 0016: the repository "About" panel (D67). Languages are measured from the default branch's files with the
-- same lexers that highlight code; these settings bound the work and say what does not count as a language.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.lang_max_files',  '20000', 'int',    strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.lang_ignore',     'Markdown,plaintext,JSON,YAML,TOML,XML,CSV,INI,Diff,reStructuredText,SVG,Org', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('ui.lang_skip_paths', 'vendor/,node_modules/,third_party/,dist/,.min.', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
