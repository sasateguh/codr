-- 0020: issues, pull requests and comments brought in from GitHub (D72). The original author is kept as the
-- GitHub login with a link to the original (never passed off as a Codr account); ext_id makes a re-import update
-- rows instead of duplicating them. Numbers keep their GitHub value: both share one sequence for issues and PRs.
ALTER TABLE issues         ADD COLUMN ext_author TEXT NOT NULL DEFAULT '';
ALTER TABLE issues         ADD COLUMN ext_url    TEXT NOT NULL DEFAULT '';
ALTER TABLE issues         ADD COLUMN ext_id     INTEGER;
ALTER TABLE pulls          ADD COLUMN ext_author TEXT NOT NULL DEFAULT '';
ALTER TABLE pulls          ADD COLUMN ext_url    TEXT NOT NULL DEFAULT '';
ALTER TABLE pulls          ADD COLUMN ext_id     INTEGER;
ALTER TABLE issue_comments ADD COLUMN ext_author TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN ext_url    TEXT NOT NULL DEFAULT '';
ALTER TABLE issue_comments ADD COLUMN ext_id     INTEGER;
ALTER TABLE pull_comments  ADD COLUMN ext_author TEXT NOT NULL DEFAULT '';
ALTER TABLE pull_comments  ADD COLUMN ext_url    TEXT NOT NULL DEFAULT '';
ALTER TABLE pull_comments  ADD COLUMN ext_id     INTEGER;
CREATE UNIQUE INDEX issues_ext         ON issues(repo_id, ext_id)        WHERE ext_id IS NOT NULL;
CREATE UNIQUE INDEX pulls_ext          ON pulls(repo_id, ext_id)         WHERE ext_id IS NOT NULL;
CREATE UNIQUE INDEX issue_comments_ext ON issue_comments(issue_id, ext_id) WHERE ext_id IS NOT NULL;
CREATE UNIQUE INDEX pull_comments_ext  ON pull_comments(pull_id, ext_id)   WHERE ext_id IS NOT NULL;
