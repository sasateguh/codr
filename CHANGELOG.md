# Changelog

Codr was developed in a private repository from 12 October 2026 and has hosted its author's own code since v0.5
(14 October 2026). This public repository starts at v2.29.0 as one squashed commit; the versions before it are listed
here only as a summary.

## v2.30.0

- `go install codr.my/codr/cmd/codr@latest` works: codr.my answers the go command's import probe.
- `codr doctor` fails when git is older than 2.40 (rebase and squash merges use `merge-tree --merge-base`).
- New installs default to English (`site.default_language = "en-US"`); existing config files are unchanged.
- Landing page: a question section (also as FAQPage data), the release binary as the first install step, schema.org data
  for the design pages, a consistent app entry for both languages, redirects for trailing slashes and `/favicon.ico`.
- Release binaries also carry version-less names, so `releases/latest/download/codr-linux-amd64` always works.
- Public CI on GitHub Actions, issue and pull request templates, a code of conduct, this changelog.

## v2.29.0 (3 November 2026)

First public release: the forge (code browsing, issues, pull requests, organisations, forks, wiki, Git LFS), CI with
`codr-runner`, REST API and webhooks, GitHub mirrors with issue import, two-step and OpenID Connect sign-in, backups to
your own S3 bucket, English and Malay interface. Release binaries for Linux amd64 and arm64.
