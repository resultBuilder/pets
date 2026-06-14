#!/bin/sh
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
APP_DIR="$ROOT_DIR/build/CodexPets.app"
CONTENTS_DIR="$APP_DIR/Contents"
MACOS_DIR="$CONTENTS_DIR/MacOS"
RESOURCES_DIR="$CONTENTS_DIR/Resources"
WEB_DIR="$RESOURCES_DIR/PetdexBrowser"
PI_EXTENSION_DIR="$RESOURCES_DIR/PiExtension"

rm -rf "$APP_DIR"
mkdir -p "$MACOS_DIR" "$WEB_DIR" "$PI_EXTENSION_DIR"

cp "$SCRIPT_DIR/Info.plist" "$CONTENTS_DIR/Info.plist"
cp "$ROOT_DIR/index.html" "$WEB_DIR/index.html"
cp "$ROOT_DIR/app.js" "$WEB_DIR/app.js"
cp "$ROOT_DIR/styles.css" "$WEB_DIR/styles.css"
cp "$ROOT_DIR/pi-extension/index.ts" "$PI_EXTENSION_DIR/index.ts"
if [ -d "$ROOT_DIR/prebundled-pets" ]; then
  cp -R "$ROOT_DIR/prebundled-pets" "$WEB_DIR/prebundled-pets"
fi

swiftc \
  -O \
  -framework Cocoa \
  -framework WebKit \
  -framework Network \
  "$SCRIPT_DIR/Sources/CodexPets/"*.swift \
  -o "$MACOS_DIR/CodexPets"

chmod +x "$MACOS_DIR/CodexPets"

# Bundle the shared Go daemon next to the app binary; the app launches it as a
# child process instead of hosting a Swift reimplementation in-process.
GO_BIN="${GO_BIN:-$(command -v go || true)}"
if [ -z "$GO_BIN" ]; then
  for candidate in /usr/local/go/bin/go /opt/homebrew/bin/go /usr/local/bin/go; do
    if [ -x "$candidate" ]; then
      GO_BIN="$candidate"
      break
    fi
  done
fi
if [ -z "$GO_BIN" ]; then
  echo "error: Go toolchain not found; install Go or set GO_BIN" >&2
  exit 1
fi
(cd "$ROOT_DIR" && "$GO_BIN" build -trimpath -o "$MACOS_DIR/pi-pet-daemon" ./cmd/pi-pet-daemon)
chmod +x "$MACOS_DIR/pi-pet-daemon"

# Write commit marker so the update checker knows which commit this binary was built from
if git -C "$ROOT_DIR" rev-parse HEAD >/dev/null 2>&1; then
  git -C "$ROOT_DIR" rev-parse HEAD > "$ROOT_DIR/build/.codex-pets-built-commit"
fi

echo "Built $APP_DIR"
