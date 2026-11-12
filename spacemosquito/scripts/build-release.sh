#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/.." && pwd)"
cd "$ROOT"

VERSION="${1:-dev}"
OUT_DIR="${OUT_DIR:-$ROOT/dist}"
mkdir -p "$OUT_DIR"

# One archive per platform: binaries, llama.cpp libraries, licenses
# (scripts/package.sh). The embedding model is published separately.
targets=(
  "darwin arm64"
  "darwin amd64"
  "linux amd64"
  "windows amd64"
)

for entry in "${targets[@]}"; do
  read -r goos goarch <<<"$entry"
  "$ROOT/scripts/package.sh" "$goos" "$goarch" "$VERSION" "$OUT_DIR"
done

"$REPO_ROOT/scripts/build-extension-zips.sh" "$VERSION" "$OUT_DIR"

(
  cd "$OUT_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum spacemosquito-* > SHA256SUMS
  else
    shasum -a 256 spacemosquito-* > SHA256SUMS
  fi
)

echo "Artifacts in $OUT_DIR"
ls -la "$OUT_DIR"
