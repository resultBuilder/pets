#!/bin/sh
set -eu

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="$ROOT_DIR/build/linux"
mkdir -p "$OUT_DIR"

if ! pkg-config --exists x11 xext; then
  echo "libX11/libXext development files are required. Install pkg-config, libx11-dev, and libxext-dev (libX11-devel/libXext-devel on Fedora)." >&2
  exit 1
fi
if pkg-config --exists gtk+-3.0 webkit2gtk-4.1; then
  WEBKIT_TAG="webkitgtk41"
elif pkg-config --exists gtk+-3.0 webkit2gtk-4.0; then
  WEBKIT_TAG="webkitgtk40"
else
  echo "GTK/WebKitGTK development files are required for the Petdex browser. Install gtk+-3.0 and webkit2gtk-4.1 (or webkit2gtk-4.0) development packages." >&2
  exit 1
fi

cd "$ROOT_DIR"
CGO_ENABLED=1 go build -tags "$WEBKIT_TAG" -o "$OUT_DIR/pi-pet-overlay" ./cmd/pi-pet-overlay

mkdir -p "$OUT_DIR/pi-extension"
cp "$ROOT_DIR/pi-extension/index.ts" "$OUT_DIR/pi-extension/index.ts"
