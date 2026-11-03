#!/usr/bin/env bash
# build-release.sh — build the release binaries exactly as published on GitHub Releases.
#   scripts/build-release.sh v2.30.0 [out-dir]   # codr + codr-runner for linux amd64/arm64, plus SHA256SUMS
# Static (CGO_ENABLED=0, pure-Go SQLite) and reproducible: the same tag and Go version give the same bytes.
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$HOME/go-toolchain/go/bin:$PATH"
TAG=${1:?usage: scripts/build-release.sh vX.Y.Z [out-dir]}; OUT=${2:-dist}
COMMIT=$(git rev-parse --short "$TAG^{commit}" 2>/dev/null || git rev-parse --short HEAD)
LD="-s -w -X codr.my/codr/internal/version.Tag=$TAG -X codr.my/codr/internal/version.Commit=$COMMIT"
mkdir -p "$OUT"
for arch in amd64 arm64; do
  for bin in codr codr-runner; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LD" -o "$OUT/$bin-$TAG-linux-$arch" "./cmd/$bin"
    # the same bytes under a version-less name, so releases/latest/download/<name> always resolves
    cp "$OUT/$bin-$TAG-linux-$arch" "$OUT/$bin-linux-$arch"
  done
done
( cd "$OUT" && sha256sum codr-*linux-* > SHA256SUMS && cat SHA256SUMS )
