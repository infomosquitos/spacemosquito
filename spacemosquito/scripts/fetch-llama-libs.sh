#!/usr/bin/env bash
# Fetch the llama.cpp shared libraries spacemosquito-embedder loads at
# runtime (ADR-019) for one platform into DEST: the pinned upstream release
# build, verified by sha256, reduced to the libraries the helper needs.
# Linux also gets libffi.so.8 from the build machine (yzma's ffi loads it).
#
# Usage: fetch-llama-libs.sh <goos> <goarch> <dest>
#   LLAMA_CACHE: download cache (default: ~/.cache/spacemosquito-llama)
#
# The build (LLAMA_TAG) and digests are the ones yzma pins in
# github.com/hybridgroup/yzma/pkg/download DefaultVersion; bump them together
# with the yzma version in go.mod.
set -euo pipefail

GOOS="${1:?usage: fetch-llama-libs.sh <goos> <goarch> <dest>}"
GOARCH="${2:?usage: fetch-llama-libs.sh <goos> <goarch> <dest>}"
DEST="${3:?usage: fetch-llama-libs.sh <goos> <goarch> <dest>}"

LLAMA_TAG="b12110"
case "$GOOS/$GOARCH" in
  darwin/arm64)
    asset="llama-${LLAMA_TAG}-bin-macos-arm64.tar.gz"
    sha="4f5b8de12e0b9516658ca6a5764c6ffb3d7c659c3dd538556b1413c7b4050662" ;;
  darwin/amd64)
    asset="llama-${LLAMA_TAG}-bin-macos-x64.tar.gz"
    sha="c8094268b6bf62f4998f0e45656fd9f5e2d4afb6b1cd0044d761db64be1dd47d" ;;
  linux/amd64)
    asset="llama-${LLAMA_TAG}-bin-ubuntu-x64.tar.gz"
    sha="368227edcc229be5f9bed16a2223d79b5e41d0ac2e19bcf5825e82291c060e55" ;;
  windows/amd64)
    asset="llama-${LLAMA_TAG}-bin-win-cpu-x64.zip"
    sha="98901e327c252875accaf2d1bf364e10e868843b0b63468baad1aa945cdc5da2" ;;
  *)
    echo "fetch-llama-libs: unsupported platform $GOOS/$GOARCH" >&2
    exit 1 ;;
esac

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

cache="${LLAMA_CACHE:-$HOME/.cache/spacemosquito-llama}"
mkdir -p "$cache"
archive="$cache/$asset"
if [ ! -f "$archive" ] || [ "$(sha256 "$archive")" != "$sha" ]; then
  echo "Downloading $asset"
  curl -fsSL -o "$archive.part" "https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_TAG}/${asset}"
  mv "$archive.part" "$archive"
fi
got="$(sha256 "$archive")"
if [ "$got" != "$sha" ]; then
  echo "fetch-llama-libs: sha256 mismatch for $asset: got $got, want $sha" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
case "$asset" in
  *.zip) unzip -q "$archive" -d "$work" ;;
  *) tar xzf "$archive" -C "$work" ;;
esac

mkdir -p "$DEST"
# Keep ggml (all backends), llama itself, the OpenMP runtime (Windows) and
# the license; drop tools, their *-impl libraries, llama-common and mtmd.
find "$work" \( -type f -o -type l \) | while read -r f; do
  name="$(basename "$f")"
  case "$name" in
    *-impl.*|*llama-common*|*mtmd*) continue ;;
    libggml*.dylib|libggml*.so*|ggml*.dll) ;;
    libllama.dylib|libllama.*.dylib|libllama.so*|llama.dll) ;;
    libomp.dll|LICENSE) ;;
    *) continue ;;
  esac
  cp -P "$f" "$DEST/$name"
done
if [ -f "$DEST/LICENSE" ]; then mv "$DEST/LICENSE" "$DEST/LICENSE-llama.cpp"; fi

if [ "$GOOS" = "linux" ]; then
  ffi=""
  for d in /usr/lib/x86_64-linux-gnu /lib/x86_64-linux-gnu /usr/lib64 /usr/lib; do
    if [ -e "$d/libffi.so.8" ]; then ffi="$d/libffi.so.8"; break; fi
  done
  if [ -n "$ffi" ]; then
    cp -L "$ffi" "$DEST/libffi.so.8"
  else
    echo "fetch-llama-libs: libffi.so.8 not found on this machine; the Linux helper will need the system's libffi" >&2
  fi
fi

echo "llama.cpp ${LLAMA_TAG} libraries for $GOOS/$GOARCH in $DEST:"
ls "$DEST"
