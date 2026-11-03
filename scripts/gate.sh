#!/usr/bin/env bash
# gate.sh — gerbang satu arahan Codr. Hijau = boleh komit. Lari sendiri, jangan petik angka.
#   ./scripts/gate.sh            # format · vet · staticcheck · gosec · govulncheck · ujian · had saiz fail
#   ./scripts/gate.sh --pasang   # pasang alat gerbang ke ~/go/bin (sekali satu hos)
#   ./scripts/gate.sh --cepat    # tanpa govulncheck (rangkaian) — untuk cangkuk pre-commit
set -euo pipefail
cd "$(dirname "$0")/.."
# ⚠ Dipanggil dari cangkuk pre-commit, git mengeksport GIT_DIR/GIT_INDEX_FILE/GIT_PREFIX… — setiap `git init`/`commit`
# dalam fixture ujian lalu menulis ke repo INI dan bukan folder sementara (gitread gagal HANYA dalam cangkuk,
# 14 Okt 2026). Buang semuanya sebelum apa-apa ujian.
for v in $(env | grep -o '^GIT_[A-Z_]*' || true); do unset "$v"; done
export PATH="$HOME/go-toolchain/go/bin:$HOME/go/bin:$PATH"
command -v go >/dev/null || { echo "go tiada — lihat TOOLCHAIN.md"; exit 2; }
HAD_BARIS=${HAD_BARIS:-800}   # D: modul kecil; satu fail Go ≤ 800 baris (PRD §4 nilai 4)

if [ "${1:-}" = "--pasang" ]; then
  go install honnef.co/go/tools/cmd/staticcheck@latest
  go install github.com/securego/gosec/v2/cmd/gosec@latest
  go install golang.org/x/vuln/cmd/govulncheck@latest
  go install github.com/git-lfs/git-lfs/v3@v3.6.1   # D82: ujian LFS memandu klien rasmi, tidak dilangkau
  echo "alat dipasang ke $HOME/go/bin"; exit 0
fi
for t in staticcheck gosec govulncheck git-lfs; do command -v $t >/dev/null || { echo "$t tiada — ./scripts/gate.sh --pasang"; exit 2; }; done

gagal=0; langkah(){ printf '▶ %s\n' "$1"; }
# ⚠ Senarai fail dari git termasuk yang BELUM ditambah (--others), dan setiap alat dipanggil dengan
# senarai itu secara eksplisit: `gofmt -l` / `wc -l` TANPA hujah membaca STDIN dan tergantung senyap
# (diukur 13 Okt 2026: gerbang pertama tergantung 20 minit pada rangka yang belum di-`git add`).
mapfile -t GOFAIL < <(git ls-files --cached --others --exclude-standard -- '*.go')
[ "${#GOFAIL[@]}" -gt 0 ] || { echo "tiada fail .go"; exit 2; }
langkah "gofmt"; F=$(gofmt -l "${GOFAIL[@]}"); [ -z "$F" ] || { echo "belum diformat:"; echo "$F"; gagal=1; }
langkah "go vet";      go vet ./... || gagal=1
langkah "staticcheck"; staticcheck ./... || gagal=1
langkah "gosec";       gosec -quiet -exclude-dir=.scratch ./... || gagal=1
if [ "${1:-}" != "--cepat" ]; then langkah "govulncheck"; govulncheck ./... || gagal=1; fi
langkah "go test";     go test -race -count=1 ./... || gagal=1
langkah "had saiz fail (≤ $HAD_BARIS baris)"
BESAR=$(wc -l "${GOFAIL[@]}" | awk -v h="$HAD_BARIS" '$1>h && $2!="total"{print $2" ("$1")"}')
[ -z "$BESAR" ] || { echo "fail terlalu besar — pecahkan:"; echo "$BESAR"; gagal=1; }
langkah "teks UI tertanam (mesti dari locale/*.json)"
# Baris cetakan dengan literal ≥ 3 perkataan di luar i18n = teks pengguna dalam kod (NFR-6). Ralat dalaman
# (errors.New/Errorf, log) dikecualikan — ia untuk log, bukan skrin.
TERTANAM=$(printf '%s\n' "${GOFAIL[@]}" | grep -v '_test\.go$' | grep -v '^internal/i18n/' \
  | xargs grep -nE 'fmt\.F?Print(ln|f)?\([^"]*"[^"]* [^"]* [^"]*"' 2>/dev/null | grep -vE '\.t\(|i18n\.T\(' || true)
[ -z "$TERTANAM" ] || { echo "teks pengguna tertanam dalam kod — pindahkan ke locale/*.json:"; echo "$TERTANAM"; gagal=1; }
langkah "bina"; go build -o /dev/null ./cmd/codr || gagal=1
# Gerbang pelayar (v0.3): Chromium desktop + WebKit iPhone 13, dua tema, axe-core, limpahan, butang Salin,
# log masuk sebenar. Dilangkau sendiri bila playwright python tiada (dicetak). --cepat melangkaunya.
if [ "${1:-}" != "--cepat" ]; then langkah "pelayar (Chromium + WebKit iPhone 13)"; python3 scripts/pelayar.py || gagal=1; fi
[ $gagal -eq 0 ] || { echo "GERBANG MERAH"; exit 1; }
# Gerbang PENUH hijau = rekod hash POKOK KERJA (indeks sementara: fail baharu + diubah, tanpa menyentuh indeks sebenar).
# deploy.sh membandingkannya dengan <tag>^{tree} (D47) — komit hijau (cangkuk --cepat) bukan bukti pelayar hijau.
if [ "${1:-}" != "--cepat" ]; then
  mkdir -p .scratch; IDX=$(mktemp); cp "$(git rev-parse --git-path index)" "$IDX" 2>/dev/null || true
  GIT_INDEX_FILE="$IDX" git add -A >/dev/null 2>&1 && GIT_INDEX_FILE="$IDX" git write-tree > .scratch/gerbang-penuh.tree
  rm -f "$IDX"; echo "penanda gerbang penuh: $(cat .scratch/gerbang-penuh.tree)"
fi
echo "GERBANG HIJAU"
