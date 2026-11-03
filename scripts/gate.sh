#!/usr/bin/env bash
# gate.sh — Codr's one-command gate. Green = ready to commit. Run it yourself; do not quote its numbers.
#   ./scripts/gate.sh            # format · vet · staticcheck · gosec · govulncheck · tests · file size · browser pass
#   ./scripts/gate.sh --pasang   # install the gate tools into ~/go/bin (once per machine)
#   ./scripts/gate.sh --cepat    # fast: no govulncheck (network) and no browser pass; used by the pre-commit hook
# The browser pass needs Python Playwright with Chromium + WebKit (`pip install playwright && playwright install`).
# Without it the full gate FAILS; CODR_SKIP_BROWSER=1 skips it on purpose (and says so).
set -euo pipefail
cd "$(dirname "$0")/.."
# Called from a pre-commit hook, git exports GIT_DIR/GIT_INDEX_FILE/GIT_PREFIX…, and every `git init`/`commit` in a test
# fixture would then write into THIS repository instead of its temp dir (measured 14 Oct 2026). Drop them all first.
for v in $(env | grep -o '^GIT_[A-Z_]*' || true); do unset "$v"; done
export PATH="$HOME/go-toolchain/go/bin:$HOME/go/bin:$PATH"
command -v go >/dev/null || { echo "go not found: install Go 1.27 or newer (scripts/pasang-go.sh <version> installs it under ~/go-toolchain)"; exit 2; }
MAX_LINES=${HAD_BARIS:-800}   # small modules: one Go file is at most 800 lines

if [ "${1:-}" = "--pasang" ]; then
  # pinned: a new release of a checker must not turn a green tree red overnight
  go install honnef.co/go/tools/cmd/staticcheck@v0.8.2
  go install github.com/securego/gosec/v2/cmd/gosec@v2.30.0
  go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
  go install github.com/git-lfs/git-lfs/v3@v3.6.1   # the LFS tests drive the official client, they are not skipped
  echo "tools installed into $HOME/go/bin"; exit 0
fi
for t in staticcheck gosec govulncheck git-lfs; do command -v $t >/dev/null || { echo "$t not found: ./scripts/gate.sh --pasang"; exit 2; }; done

failed=0; step(){ printf '▶ %s\n' "$1"; }
# The file list comes from git, including files not yet added (--others), and is passed to every tool explicitly:
# `gofmt -l` / `wc -l` WITHOUT arguments read STDIN and hang silently (measured 13 Oct 2026: 20 minutes).
mapfile -t GOFILES < <(git ls-files --cached --others --exclude-standard -- '*.go')
[ "${#GOFILES[@]}" -gt 0 ] || { echo "no .go files"; exit 2; }
step "gofmt"; F=$(gofmt -l "${GOFILES[@]}"); [ -z "$F" ] || { echo "not formatted:"; echo "$F"; failed=1; }
step "go vet";      go vet ./... || failed=1
step "staticcheck"; staticcheck ./... || failed=1
step "gosec";       gosec -quiet -exclude-dir=.scratch ./... || failed=1
if [ "${1:-}" != "--cepat" ]; then step "govulncheck"; govulncheck ./... || failed=1; fi
step "go test";     go test -race -count=1 ./... || failed=1
step "file size (at most $MAX_LINES lines)"
BIG=$(wc -l "${GOFILES[@]}" | awk -v h="$MAX_LINES" '$1>h && $2!="total"{print $2" ("$1")"}')
[ -z "$BIG" ] || { echo "files too large, split them:"; echo "$BIG"; failed=1; }
step "no UI text in code (it lives in locale/*.json)"
# A print with a literal of three or more words outside i18n is user text in code. Internal errors
# (errors.New/Errorf, logs) are exempt: they are for logs, not screens.
EMBEDDED=$(printf '%s\n' "${GOFILES[@]}" | grep -v '_test\.go$' | grep -v '^internal/i18n/' \
  | xargs grep -nE 'fmt\.F?Print(ln|f)?\([^"]*"[^"]* [^"]* [^"]*"' 2>/dev/null | grep -vE '\.t\(|i18n\.T\(' || true)
[ -z "$EMBEDDED" ] || { echo "user text embedded in code, move it to locale/*.json:"; echo "$EMBEDDED"; failed=1; }
step "build"; go build -o /dev/null ./cmd/codr || failed=1
# Browser pass: Chromium desktop + WebKit iPhone 13, both themes, axe-core, overflow, the Copy buttons, a real sign-in.
if [ "${1:-}" != "--cepat" ]; then
  if [ "${CODR_SKIP_BROWSER:-}" = "1" ]; then echo "▶ browser pass SKIPPED (CODR_SKIP_BROWSER=1): this is not a full gate"
  else step "browser (Chromium + WebKit iPhone 13)"; python3 scripts/pelayar.py || failed=1; fi
fi
[ $failed -eq 0 ] || { echo "GATE RED"; exit 1; }
# A green FULL gate records the hash of the WORKING TREE (temporary index: new + changed files, the real index untouched).
# A release is only tagged from a tree with this mark; a green commit (hook = --cepat) is not proof of a green browser pass.
if [ "${1:-}" != "--cepat" ] && [ "${CODR_SKIP_BROWSER:-}" != "1" ]; then
  mkdir -p .scratch; IDX=$(mktemp); cp "$(git rev-parse --git-path index)" "$IDX" 2>/dev/null || true
  GIT_INDEX_FILE="$IDX" git add -A >/dev/null 2>&1 && GIT_INDEX_FILE="$IDX" git write-tree > .scratch/gerbang-penuh.tree
  rm -f "$IDX"; echo "full gate mark: $(cat .scratch/gerbang-penuh.tree)"
fi
echo "GATE GREEN"
