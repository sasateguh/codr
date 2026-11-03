-- 0031: closing issues from commits and pull requests (D87, FR-21). A third reader of the event outbox (cursor 'xref')
-- reads pushes to a default branch and merged pull requests, finds "fixes #n" style keywords and closes those issues.
-- closed_by_ref names the commit (or "#<pull>" for a pull request) that closed an issue, so the page can say so;
-- reopening clears it.
ALTER TABLE issues ADD COLUMN closed_by_ref TEXT NOT NULL DEFAULT '';
-- the reader starts at the newest event: pushes from before this release close nothing
INSERT INTO event_cursors (name, last_id) VALUES ('xref', (SELECT COALESCE(MAX(id), 0) FROM events));
INSERT INTO settings (key, value, type, updated_at) VALUES
  ('issue.close_keywords',      'close,closes,closed,fix,fixes,fixed,resolve,resolves,resolved,tutup,menutup,baiki,membaiki', 'string', strftime('%Y-%m-%dT%H:%M:%fZ','now')),
  ('issue.close_scan_commits',  '250', 'int', strftime('%Y-%m-%dT%H:%M:%fZ','now'));
