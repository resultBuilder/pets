#!/bin/sh
set -eu
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
BUILD_DIR="${TMPDIR:-/tmp}/codex-pets-tests"
mkdir -p "$BUILD_DIR"

# Build the real Go daemon so the Swift protocol tests run against the same
# binary the app bundles.
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
(cd "$ROOT_DIR" && "$GO_BIN" build -o "$BUILD_DIR/pi-pet-daemon" ./cmd/pi-pet-daemon)

swiftc \
  -framework Cocoa \
  -framework WebKit \
  -framework Network \
  "$SCRIPT_DIR/Sources/CodexPets/PetModels.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/PetdexBrowser.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/DaemonClient.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/DaemonProcess.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/StateServer.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/PetBrain.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/UpdateStatus.swift" \
  "$SCRIPT_DIR/Sources/CodexPets/PetOverlay.swift" \
  "$SCRIPT_DIR/tests/PetBrainTests/main.swift" \
  -o "$BUILD_DIR/PetBrainTests"

cd "$ROOT_DIR"
CODEX_PETS_TEST_DAEMON_BIN="$BUILD_DIR/pi-pet-daemon" "$BUILD_DIR/PetBrainTests"
