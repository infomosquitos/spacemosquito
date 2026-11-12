#!/usr/bin/env bash
# Build one platform's release archive (ADR-019):
#   spacemosquito-<goos>-<goarch>.tar.gz (.zip for Windows) containing
#   spacemosquito, spacemosquito-embedder, lib/ (llama.cpp), LICENSE and
#   THIRD_PARTY_LICENSES. The embedding model ships separately (model cask /
#   release asset).
#
# Usage: package.sh <goos> <goarch> <version> <outdir>
set -euo pipefail

GOOS="${1:?usage: package.sh <goos> <goarch> <version> <outdir>}"
GOARCH="${2:?usage: package.sh <goos> <goarch> <version> <outdir>}"
VERSION="${3:?usage: package.sh <goos> <goarch> <version> <outdir>}"
OUT_DIR="${4:?usage: package.sh <goos> <goarch> <version> <outdir>}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/.." && pwd)"
cd "$ROOT"
mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

LDFLAGS="-s -w -X github.com/vkh/spacemosquito/internal/cliapp.Version=${VERSION}"
exe=""
[ "$GOOS" = "windows" ] && exe=".exe"

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

echo "Building spacemosquito $VERSION for $GOOS/$GOARCH"
for cmd in spacemosquito spacemosquito-embedder; do
  GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stage/$cmd$exe" "./cmd/$cmd"
done

"$ROOT/scripts/fetch-llama-libs.sh" "$GOOS" "$GOARCH" "$stage/lib" >/dev/null

cp "$REPO_ROOT/LICENSE" "$stage/LICENSE"
{
  echo "spacemosquito bundles the following third-party software."
  echo
  section() {
    echo "================================================================"
    echo "$1"
    echo "================================================================"
    cat "$2"
    echo
  }
  if [ -f "$stage/lib/LICENSE-llama.cpp" ]; then
    section "llama.cpp and ggml (lib/) - https://github.com/ggml-org/llama.cpp" "$stage/lib/LICENSE-llama.cpp"
  else
    echo "llama.cpp and ggml (lib/) - https://github.com/ggml-org/llama.cpp - MIT License"
    echo
  fi
  for mod in github.com/hybridgroup/yzma github.com/ebitengine/purego github.com/jupiterrider/ffi; do
    dir="$(go list -m -f '{{.Dir}}' "$mod")"
    for f in LICENSE COPYRIGHT.txt assets/libffi/LICENSE; do
      [ -f "$dir/$f" ] && section "$mod ($f)" "$dir/$f"
    done
  done
} > "$stage/THIRD_PARTY_LICENSES"
rm -f "$stage/lib/LICENSE-llama.cpp"

name="spacemosquito-${GOOS}-${GOARCH}"
if [ "$GOOS" = "windows" ]; then
  rm -f "$OUT_DIR/$name.zip"
  (cd "$stage" && zip -qry "$OUT_DIR/$name.zip" .)
  echo "Wrote $OUT_DIR/$name.zip"
else
  # Symlinks in lib/ (libllama.dylib -> libllama.0.dylib …) are kept.
  tar czf "$OUT_DIR/$name.tar.gz" -C "$stage" .
  echo "Wrote $OUT_DIR/$name.tar.gz"
fi
