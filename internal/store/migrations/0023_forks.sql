-- 0023: forks (D75). A fork records the repository it came from (the link is cleared, not cascaded, when the
-- source is deleted: the fork stays someone's code). A pull request may come from another repository: its
-- head branch then lives in head_repo_id and is fetched into the base repository under refs/codr/pull/<id>.
ALTER TABLE repos ADD COLUMN fork_of INTEGER REFERENCES repos(id) ON DELETE SET NULL;
ALTER TABLE pulls ADD COLUMN head_repo_id INTEGER REFERENCES repos(id) ON DELETE SET NULL;
CREATE INDEX repos_fork_of ON repos(fork_of) WHERE fork_of IS NOT NULL;
