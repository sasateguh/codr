# How Codr is built

Codr is a git forge that one person can run and understand: one binary, one database file, one folder of bare
repositories. This page explains the choices behind it and the rules the code keeps. Every choice has a numbered
decision record (D1, D2, …) kept by the maintainers, with what it replaced and how to undo it; the numbers in
code comments point to it.

## One binary, git does the git

The forge is a single Go program, `codr`. It serves the web pages, the HTTPS and SSH git doors, the API and the hooks.
It does not parse git's pack format or wire protocol itself. Clones and pushes run the real `git` binary
(`git upload-pack`, `git receive-pack`, `git http-backend`), so every git client keeps working and git's own security
fixes apply on the day they ship (D2).

Writes the web makes to a repository (merges, squash and rebase merges, wiki pages) use git plumbing on the bare
repository: `merge-tree` builds the result in memory, `commit-tree` writes the commit, and `update-ref` moves the branch
only if it still points where the page read it. There is no working tree, no lock file to clean up, and a branch that
moved in between is refused rather than overwritten (D53, D83, D88).

## One database file

Everything that is not git lives in one SQLite file in WAL mode: accounts, permissions, issues, pull requests,
notifications, audit rows, settings (D3). Migrations only ever add. Repositories on disk are named by their database id
(`repos/<id>.git`), never by a name a person typed, so renaming or a hostile name cannot reach the file system (D27).

Backups are consistent snapshots: the database through SQLite's `VACUUM INTO`, each repository as a verified git bundle,
large files (LFS) and wiki repositories alongside. Every night the backup is restored into an empty folder and checked,
because a backup that was never restored is a hope, not a backup (D41).

## One permission decision

Who may read or write a repository is answered by one function, `AccessFor`, and every door asks it before git runs:
SSH, HTTPS, the web pages and the API. A private repository a person cannot read answers "not found", never
"forbidden", so its existence does not leak. Organisations and teams feed the same function (D74). Tests compare it with
the list query that decides what a person sees, so the two cannot drift apart.

The same rule follows everything that shows content to someone else: notifications are only written for people who can
read the repository, a profile's activity is filtered by the visitor's access, and an `owner/repo#12` reference becomes a
link only for readers of that repository (D68, D79, D87).

## Push rules in one place

Protected branches, size limits and required checks are enforced by a git hook that calls back into the running server
over a local socket, so the HTTPS and SSH doors share one implementation (D31). A protected branch can refuse force pushes,
deletion, and, when asked, any commit whose CI checks have not all passed (D89).

## Events, written once

Every change people care about (a push, an issue opened, a review, a merge) is written once to an outbox table, next to
its audit row. Independent readers follow that outbox with their own cursor: webhook delivery (HMAC signed, retried,
checked against private addresses after DNS resolution), notifications and email, and the reader that closes issues named
by `fixes #12` once the commit reaches the default branch. A slow reader never delays a push (D78, D79, D87).

## CI outside the forge

`codr-runner` is a separate small program. It receives push webhooks, checks their signature, fetches the commit and runs
the jobs listed in `.codr/ci.toml`, each in a container with no network, no capabilities, a read-only root file system and
bounded memory, CPU and processes. Results come back as commit statuses through the API, with a link to the job log
(D89). The forge does not run user code itself.

## Secrets

Passwords are hashed with Argon2id. Session and access tokens are stored as hashes; a token is shown once. Secrets the
server must use later (mirror tokens, webhook secrets, the mail password, backup keys) are sealed with a key that lives in
the configuration file, not in the database, so a copy of the database alone opens nothing (D28, D38, D70). Sign-in can
require a TOTP code; the step between password and code is a challenge, not a session (D80).

## Text and settings

There are no user-facing strings in the code. Every sentence lives in `locale/ms-MY.json` and `locale/en-US.json`, and the
build gate fails when a template or an error message carries literal text (D34). Values that reasonably change (page
sizes, limits, timeouts, keyword lists) are settings in the database, editable without a deploy. Malay and English are
equal from the first page.

## The gate

Nothing is tagged until the full gate passes on that exact code tree: formatting, `go vet`, `staticcheck`, `gosec`,
`govulncheck`, the race-detecting test suite, a size limit per file, the scan for hard-coded text, and real browsers
(Chromium, and WebKit as an iPhone 13) in both themes with an accessibility check on every page. The deploy script refuses
a tag whose tree has no record of a full green run, keeps the previous version, and rolls back on its own when the new one
does not answer with its version (D42, D47).

## What it does not try to be

Codr is not a hosting service for many tenants, does not try to match GitHub Actions feature for feature, and does not run
an inbound mail server. Those limits are written down so they stay limits until there is a reason to change them.
