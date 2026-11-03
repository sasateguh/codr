# Contributing to Codr

Codr is a self-hosted git forge built in Malaysia. Code, comments, commit messages and the README are in English;
the maintainers' planning notes are in Malay (Bahasa Melayu). The product UI is English by default
and fully Malay, in both themes.

- Run `./scripts/gate.sh` before every pull request (install its tools once with `./scripts/gate.sh --pasang`). A release is only tagged from a tree that passed the full gate.
- Code comments cite design decisions as D1, D2, … These point to the maintainers' decision log, which is not public yet;
  if a comment's reason is unclear, ask in an issue. A change that reverses a decision gets a new number; old ones are never edited.
- The browser pass of the gate (`scripts/pelayar.py`) still prints its progress in Malay. Translations are welcome.
- No UI strings in Go code or templates: all text lives in `locale/*.json`, in both languages.
- No "reasonable to change" literal (limits, names, schedules) in code: it belongs in `config.toml`
  (infrastructure) or the `settings` table (product).
- Sign your commits with `git commit -s` ([Developer Certificate of Origin](https://developercertificate.org)). No CLA.
- Be kind: see the [code of conduct](CODE_OF_CONDUCT.md). Security problems go to [SECURITY.md](SECURITY.md), not to issues.
- Licence: AGPL-3.0-or-later. By contributing you agree your contribution is licensed the same way.
