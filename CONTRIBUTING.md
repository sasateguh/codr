# Contributing to Codr

Codr is a self-hosted git forge built in Malaysia. Code, comments, commit messages and the README are in English;
the maintainers' planning notes are in Malay (Bahasa Melayu). The product UI is English by default
and fully Malay, in both themes.

- Run `./scripts/gate.sh` before every pull request (install its tools once with `./scripts/gate.sh --pasang`). A release is only tagged from a tree that passed the full gate.
- A change that reverses an earlier design decision is recorded as a new numbered decision (D1, D2, … in the maintainers' log, cited in code comments); old decisions are never edited.
- No UI strings in Go code or templates: all text lives in `locale/*.json`, in both languages.
- No "reasonable to change" literal (limits, names, schedules) in code: it belongs in `config.toml`
  (infrastructure) or the `settings` table (product).
- Sign your commits with `git commit -s` (Developer Certificate of Origin). No CLA.
- Licence: AGPL-3.0-or-later. By contributing you agree your contribution is licensed the same way.
