#!/bin/sh
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
APP_DIR="$ROOT_DIR/build/CodexPets.app"
APP_BINARY="$APP_DIR/Contents/MacOS/CodexPets"
DAEMON_BINARY="$APP_DIR/Contents/MacOS/pi-pet-daemon"

matching_pids() {
  command_path="$1"
  ps -axo pid=,command= | awk -v command_path="$command_path" '
    {
      pid = $1
      command = $0
      sub(/^[[:space:]]*[0-9]+[[:space:]]+/, "", command)
      if (command == command_path || index(command, command_path " ") == 1) {
        print pid
      }
    }
  '
}

wait_until_stopped() {
  command_path="$1"
  timeout_seconds="$2"
  deadline=$(( $(date +%s) + timeout_seconds ))
  while [ -n "$(matching_pids "$command_path")" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep 0.2
  done
}

wait_until_started() {
  command_path="$1"
  timeout_seconds="$2"
  deadline=$(( $(date +%s) + timeout_seconds ))
  while [ -z "$(matching_pids "$command_path")" ]; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep 0.2
  done
}

terminate_command() {
  command_path="$1"
  signal="$2"
  pids="$(matching_pids "$command_path")"
  if [ -n "$pids" ]; then
    echo "$pids" | xargs kill "-$signal" 2>/dev/null || true
  fi
}

stop_running_app() {
  if [ -z "$(matching_pids "$APP_BINARY")$(matching_pids "$DAEMON_BINARY")" ]; then
    return
  fi

  echo "Stopping running CodexPets..."
  osascript -e 'tell application "CodexPets" to quit' >/dev/null 2>&1 || true

  if ! wait_until_stopped "$APP_BINARY" 8; then
    terminate_command "$APP_BINARY" TERM
    wait_until_stopped "$APP_BINARY" 3 || terminate_command "$APP_BINARY" KILL
  fi

  if ! wait_until_stopped "$DAEMON_BINARY" 3; then
    terminate_command "$DAEMON_BINARY" TERM
    wait_until_stopped "$DAEMON_BINARY" 2 || terminate_command "$DAEMON_BINARY" KILL
  fi
}

cd "$ROOT_DIR"
stop_running_app

echo "Building CodexPets..."
sh "$SCRIPT_DIR/build.sh"

echo "Launching CodexPets..."
open "$APP_DIR"

if ! wait_until_started "$APP_BINARY" 15; then
  echo "error: CodexPets did not start from $APP_DIR" >&2
  exit 1
fi

echo "Started CodexPets: $(matching_pids "$APP_BINARY" | tr '\n' ' ')"
if wait_until_started "$DAEMON_BINARY" 10; then
  echo "Started pi-pet-daemon: $(matching_pids "$DAEMON_BINARY" | tr '\n' ' ')"
else
  echo "warning: CodexPets started, but pi-pet-daemon was not observed yet" >&2
fi
