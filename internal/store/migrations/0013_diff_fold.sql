-- 0013: a file whose diff has more lines than this is folded on commit and pull request pages (D61);
-- one click opens it. 0 = never fold.
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('ui.diff_fold_lines', '400', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
