#!/usr/bin/env bash
# pasang-go.sh <versi> — pasang/naik Go di ~/go-toolchain/go (pengguna, tanpa sudo), sha256 disahkan dari go.dev.
set -euo pipefail
V=${1:?versi, cth 1.27.2}; D=$HOME/go-toolchain; K=$(mktemp -d); trap 'rm -rf "$K"' EXIT
curl -sSL -o "$K/go.tgz" "https://go.dev/dl/go$V.linux-amd64.tar.gz"
curl -sSL "https://go.dev/dl/?mode=json&include=all" | python3 -I -c "import json,sys; print(next(f['sha256'] for v in json.load(sys.stdin) if v['version']=='go$V' for f in v['files'] if f['filename']=='go$V.linux-amd64.tar.gz'))" > "$K/sha"
echo "$(cat "$K/sha")  $K/go.tgz" | sha256sum -c - >/dev/null
mkdir -p "$D"; rm -rf "$D/go.baru"; mkdir "$D/go.baru"; tar -C "$D/go.baru" --strip-components=1 -xzf "$K/go.tgz"
rm -rf "$D/go.lama"; [ -d "$D/go" ] && mv "$D/go" "$D/go.lama"; mv "$D/go.baru" "$D/go"; rm -rf "$D/go.lama"
"$D/go/bin/go" version
