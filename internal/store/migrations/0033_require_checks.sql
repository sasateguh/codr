-- 0033: a protected branch may require green checks (D89, Fasa 3a): the commit a branch moves to must carry at least
-- one commit status and every status on it must be success, both for a push (pre-receive) and for a pull request merge.
ALTER TABLE protected_branches ADD COLUMN require_checks INTEGER NOT NULL DEFAULT 0;
