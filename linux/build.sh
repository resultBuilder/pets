#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="$ROOT_DIR/build/linux"
mkdir -p "$OUT_DIR"

if ! pkg-config --exists x11 xext; then
  echo "libX11/libXext development files are required. Install pkg-config, libx11-dev, and libxext-dev (libX11-devel/libXext-devel on Fedora)." >&2
  exit 1
fi

cd "$ROOT_DIR"
CGO_ENABLED=1 go build -o "$OUT_DIR/pi-pet-overlay" ./cmd/pi-pet-overlay

mkdir -p "$OUT_DIR/pi-extension"
cp "$ROOT_DIR/pi-extension/index.ts" "$OUT_DIR/pi-extension/index.ts"
