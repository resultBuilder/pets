# Codex Pets

A small project for Codex-compatible pets:

- A compact WebView Petdex picker for browsing and importing pets.
- A native macOS status-bar app that shows an imported pet in an always-on-top transparent overlay.

## Native macOS App

Build the `.app` bundle with the Swift command-line toolchain:

```sh
sh macos/CodexPets/build.sh
```

Open it:

```sh
open build/CodexPets.app
```

The app appears as a `CP` status-bar item. Use that menu to open Petdex, import a pet folder, switch pets, choose animation states, resize the overlay, or quit.

The menu also includes Calm Pet Engine controls:

- **Animation:** Focus, Default, or Playful attention budgets.
- **Bubbles / Pet Murmurs:** Silent, Quiet, Default, or Chatty. Default is intentionally calm: short curated replies, daily budgets, global cooldowns, semantic anti-repeat, and no raw workflow labels for known Codex events.
- **Mute Murmurs Today:** clears the current murmur and keeps the pet quiet until tomorrow.
- **Reduced Motion:** follow macOS Reduce Motion or force static poses.
- **Full-screen:** hidden in full-screen apps by default; opt in from Settings.

Pet Murmurs are stored locally in:

```text
~/Library/Application Support/CodexPets/DialogueHistory.json
```

Clicking a visible murmur bubble dismisses it and mutes additional murmurs for a few hours. Direct `/bubble` API messages still display as explicit caller-provided text.

The transparent overlay is click-through outside the real pet sprite, so padding around the window does not block apps underneath. Mouse proximity, hover, click, double-click, drag, and spam-click reactions work without Accessibility or Input Monitoring permissions.

Use **Browse Petdex...** from the CP menu to open the WebView-backed Petdex picker. It presents a native-style split view with search, a pet list, a preview, and compact actions. Petdex pets are downloaded into local app storage with **Import**; installed pets can be selected with **Use**. The footer shows the Pi extension status. **Install to Pi** copies the bundled Pi extension to `~/.pi/agent/extensions/codex-pets.ts`, **Update Pi** appears when the bundled extension changes, and **Uninstall Pi** removes the installed extension so it can be reinstalled. All three actions are served by the daemon's `pi.extension.*` protocol methods; the app only relays results into the WebView.

Imported pets are copied to:

```text
~/Library/Application Support/CodexPets/Pets
```

The app also scans existing compatible installs:

```text
~/.petdex/pets
~/.codex/pets
```

The legacy local HTTP state API is disabled by default. For manual debugging only, launch the app with:

```sh
CODEX_PETS_ENABLE_HTTP_STATE_API=1 ./build/CodexPets.app/Contents/MacOS/CodexPets
```

When enabled, it writes connection info here:

```text
~/Library/Application Support/CodexPets/Runtime/state-api.json
```

Example state update:

```sh
TOKEN=$(cat "$HOME/Library/Application Support/CodexPets/Runtime/state-token")
curl -X POST http://127.0.0.1:7777/state \
  -H "content-type: application/json" \
  -H "x-codex-pets-token: $TOKEN" \
  -d '{"state":"running","duration":1200}'
```

Example bubble:

```sh
TOKEN=$(cat "$HOME/Library/Application Support/CodexPets/Runtime/state-token")
curl -X POST http://127.0.0.1:7777/bubble \
  -H "content-type: application/json" \
  -H "x-codex-pets-token: $TOKEN" \
  -d '{"text":"Working on it"}'
```

Example workflow event:

```sh
TOKEN=$(cat "$HOME/Library/Application Support/CodexPets/Runtime/state-token")
curl -X POST http://127.0.0.1:7777/event \
  -H "content-type: application/json" \
  -H "x-codex-pets-token: $TOKEN" \
  -d '{"type":"task.succeeded","label":"Tests passed","importance":"low"}'
```

Supported states are `idle`, `running`, `running-left`, `running-right`, `waving`, `jumping`, `failed`, `waiting`, and `review`.

Supported event types include `task.succeeded`, `task.needs_user`, `review`, and `task.failed`. `/state` remains the backward-compatible debug shortcut; `/event` is preferred for one-shot success, waiting/review, and failure moments. Known workflow events show curated Pet Murmurs instead of raw labels; use `/bubble` when you explicitly want to display caller-provided text.

Run the native tests:

```sh
sh macos/CodexPets/test.sh
```

## Browser Gallery

Run:

```sh
/Users/anna/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node server.mjs
```

Open <http://localhost:4173>.

## Petdex Support

- Loads only the local bundled catalog from `prebundled-pets/manifest.json`.
- Bundled app builds copy `prebundled-pets` into the WebView bundle and do not copy `vendor/petdex`.
- The native browser bridge imports pet assets only from the bundled WebView directory.
- Imports custom folders or paired files containing `pet.json` and `spritesheet.webp` or `spritesheet.png`.
- Stores custom pets in IndexedDB so spritesheets survive a refresh.

To create a local Petdex snapshot for manual asset curation:

```sh
npm run dump:petdex
```

This writes `vendor/petdex/manifest.json`, `manifest.remote.json`, `source-manifest.json`, and downloaded pet assets under `vendor/petdex/pets/`. Those files are not loaded by the app automatically; copy curated pets into `prebundled-pets/pets/` and add them to `prebundled-pets/manifest.json`.

Pet package rendering follows Petdex's 8-column by 9-row atlas format with `192x208` default frames.

Native app imports use the same folder shape:

```text
my-pet/
  pet.json
  spritesheet.webp
```

`spritesheet.png`, `sprite.webp`, and `sprite.png` are also accepted.

## Pi Pet v1 Components

The cross-platform Pi Pet v1 work is split into native pieces instead of using Electron:

- `cmd/pi-pet-daemon`: the single shared Go daemon over a Unix domain socket. The macOS app bundles this binary and supervises it as a child process (`macos/CodexPets/Sources/CodexPets/DaemonProcess.swift`); it also runs standalone for headless/testing flows. With `-watch-stdin` it exits when its supervising parent closes the pipe, so it cannot outlive the app. Every snapshot it publishes embeds the canonical overlay `presentation` (state id, bubble, auto-clear) computed by `internal/overlay`, so native overlays only render it. It also owns Pi extension management through the `pi.extension.status/install/uninstall` protocol methods (`-pi-extension-source` points at the bundled extension; `-pi-extension-autoupdate` refreshes a stale install at startup) and persists the selected pet across restarts in `-state-file` (defaults to the platform config directory; the macOS app points it at `~/Library/Application Support/CodexPets/daemon-state.json`).
- `internal/updater`: the shared self-update pipeline behind the `update.check/update.apply/update.dismiss` protocol methods. The daemon detects upstream commits (or a diverged built-commit marker, even offline), fast-forwards the checkout, runs the host build script (`-build-script`, relative to the autodetected repo root), and records the built commit; progress is published in the snapshot `update` field. Hosts only drive the check cadence and restart themselves when the stage reaches `restartPending` — on macOS that is the only remaining native piece (`UpdateStatus.swift`), and the Linux binary wires the same updater with `linux/build.sh`.
- `internal/protocol`: newline-delimited JSON protocol shared by Pi extension, daemon hosts, overlays, and browser bridge.
- `internal/daemon`: Go session state machine, approval broker, selected/installed pet state, catalog cache state, and subscriber broadcasts for app-owned and standalone daemon hosts.
- `pi-extension/index.ts`: Pi TypeScript extension that observes session/agent/tool lifecycle events and blocks risky bash calls until daemon approval returns.
- `internal/catalog`: Petdex manifest normalization, local `.codex/pets` and `.petdex/pets` importer validation, and PadX provider stub behind the same provider interface. It also owns the shared installed-pet domain used by the daemon: scanning pet roots into wire refs with macOS-compatible `source:slug:dir` ids, deduplicating duplicate packages of the same pet (canonical slug/directory wins), validated atomic `pet.import` writes (canonicalized slug and manifest, MIME/size checks, staging + rename), and `pet.uninstall` constrained to the import root. The daemon serves these through `pets.refresh`, `pet.import` (raw bytes or a local package `directory`), and `pet.uninstall`, scans `-pets-root source:path` directories at startup (`-pets-import-root` receives imports), and applies the same dedupe to legacy `pets.installed.set` pushes. It also owns the picker view: `-catalog-dir` points at the bundled catalog and `pets.browser.list` returns the merged installed+catalog rows with duplicate catalog entries already hidden. Pet refs are fully enriched (slug, description, kind, spritesheet path, frame size), so hosts never parse pet.json: the macOS app maps refs straight into render packages, relays folder/catalog imports as directory-based `pet.import`, and the web picker renders daemon rows verbatim in the native shell (its own manifest parsing remains only as the standalone web fallback).
- `macos/CodexPets`: current AppKit overlay and WebKit pet browser.
- `internal/petbrain`: the pet's shared temperament — the murmur phrase book and dialogue engine (rarity weighting, semantic-group/daily/global cooldowns, mute-for-today, history persisted in the same JSON the macOS app used), plus interaction reactions (click/petting/spam-click, drag, hover, return greetings) and idle pacing budgets. The daemon attaches it to the store: murmurs merge into the presentation bubble when no status bubble is showing, interactions arrive via `overlay.interaction` and produce short presentation pulses (a wave on click), temperament is tuned with `overlay.settings.set` (persisted in the daemon state file), and `murmurs.mute` silences the pet for the day or a few hours. The Swift `PetBrain` keeps only pose/pacing decisions for the native renderer; all murmur text lives here, so the Linux pet has the same personality.
- `internal/render`: the platform-independent overlay compositor. It loads PNG and WebP pet packages through the shared catalog validation, slices atlas rows with the same animation table as the macOS app, and composes the complete overlay frame (sprite, UTF-8 speech bubble, session chip, update pill) into an RGBA buffer plus interactive hit regions — all testable without a display server.
- `internal/overlayhost`: the platform-independent overlay run loop shared by every Linux backend — snapshot subscription, animation pacing, bubble auto-clear, interaction relays, the update pill, and self-restart after updates. Backends implement a small interface (open surface, present frame, set input regions, pump semantic pointer events).
- `internal/wayland` + `internal/waylandoverlay`: first-class Wayland support as a dependency-free pure-Go client (the wire protocol over the Unix socket with fd passing — no libwayland, no cgo). The pet is a `wlr-layer-shell` surface anchored bottom-right: native per-pixel transparency (premultiplied ARGB wl_shm buffers), true always-above placement, click-through via `wl_surface.set_input_region`, drag by adjusting layer margins, and integer HiDPI buffer scaling from `wl_output`. Works on wlroots compositors (Sway, Hyprland, river, Wayfire), KDE Plasma, and anything else implementing layer-shell; covered by tests against a mock compositor (`internal/wayland/wltest`), so the protocol flow runs in CI on any OS.
- `cmd/pi-pet-overlay`: the unified Linux host. It starts the daemon socket in-process, scans pet packages into the daemon at startup, and picks a backend automatically: native Wayland when `WAYLAND_DISPLAY` is set and the compositor offers layer-shell, otherwise X11/XWayland (covers GNOME, which lacks layer-shell). Override with `-backend wayland|x11`. The X11 backend keeps its 32-bit ARGB visual with true transparency under a compositor (shaped opaque fallback without one), double-buffered presents, XShape click-through, and DPI-based `-scale` autodetection.

On macOS, build and launch the app; it launches the bundled Go daemon (`Contents/MacOS/pi-pet-daemon`) as a supervised child process that owns the socket:

```sh
sh macos/CodexPets/build.sh
./build/CodexPets.app/Contents/MacOS/CodexPets
```

The app-supervised daemon listens only on:

```text
$XDG_RUNTIME_DIR/pi-pet.sock
```

or, when `XDG_RUNTIME_DIR` is unset:

```text
/tmp/codex-pets-$UID/pi-pet.sock
```

The Pi extension uses the same path. Override it with `PI_PET_SOCKET_DIR` for tests or custom launchers. Install the extension by adding `pi-extension/index.ts` to Pi's extension paths, for example through Pi settings or by copying/symlinking it into `~/.pi/agent/extensions/`.

On Linux X11, build and run the overlay app; it also starts the daemon socket inside the app process:

```sh
sh linux/build.sh
./build/linux/pi-pet-overlay
```

Right-click the pet body to open the native WebKitGTK Petdex picker. For scripted checks or direct launch, use:

```sh
./build/linux/pi-pet-overlay -open-petdex
./build/linux/pi-pet-overlay -petdex-only
```

Install the Pi extension from the Linux build with:

```sh
./build/linux/pi-pet-overlay -install-pi-extension
```

Uninstall it from the same location with:

```sh
./build/linux/pi-pet-overlay -uninstall-pi-extension
```

The standalone Go daemon can still be run with `go run ./cmd/pi-pet-daemon` for headless protocol checks.

Current verification:

```sh
npm test
node --check app.js
npm run test:browser-smoke
npm run test:macos-gui-smoke
npm run test:linux-gui-smoke
sh macos/CodexPets/test.sh
sh macos/CodexPets/build.sh
env GOCACHE=/tmp/codex-pets-go-build GOOS=linux CGO_ENABLED=0 go build -o /tmp/pi-pet-overlay-check ./cmd/pi-pet-overlay
```

Latest checkpoint results on macOS:

- `npm test`: passed; covers Go protocol, daemon attention state and embedded overlay presentation, tool progress updates, selected/installed pet state, catalog cache state, approval broker/integration flow, golden wire-level protocol fixtures (`internal/daemon/testdata/golden`, including expected presentations), Pi extension install/status/version/auto-update logic and the `pi.extension.*` socket methods, Petdex provider normalization, Linux X11 PNG atlas/frame/text helper logic, Pi extension unit tests, turn/tool lifecycle hooks, abort-aware approval, serialized notification delivery, and a scripted Pi-extension-to-real-daemon-to-overlay Unix-socket approval flow.
- `node --check app.js`: passed; verifies the bundled WebView picker script parses after native bridge and installed-pet edits.
- `npm run test:browser-smoke`: passed; renders the bundled WebView picker in local headless Chrome, verifies installed pet spritesheet preview, Pi extension status/install/uninstall, and installed-pet selection bridge messages, and writes `/tmp/codex-pets-browser-smoke.png`.
- `npm run test:macos-gui-smoke`: passed; builds and launches the real AppKit app, verifies the app spawns the bundled Go daemon and its Unix socket, sends fake Pi `running`, `failed`, and `approval_required` events to that socket, and verifies native overlay-state mapping.
- `sh macos/CodexPets/test.sh`: passed; covers PetBrain, Petdex parser/importer, WebView bridge action allowlist and payload validation, installed-pet bridge payloads, wire-presentation decoding, the bundled Go daemon's Unix-socket protocol handling driven through `DaemonProcessController`, and overlay hit testing. Pi extension install/status logic is covered by the Go `internal/piinstall` and daemon socket tests.
- `sh macos/CodexPets/build.sh`: passed; produced `build/CodexPets.app` with the bundled `pi-pet-daemon` Go binary (requires a Go toolchain; override with `GO_BIN`).
- `env GOCACHE=/tmp/codex-pets-go-build GOOS=linux CGO_ENABLED=0 go build -o /tmp/pi-pet-overlay-check ./cmd/pi-pet-overlay`: passed; verifies the Linux overlay command wrapper, Pi extension install/uninstall flags, and non-cgo fallback compile from macOS.

## Security Model

- Both platforms run the same Go daemon (`cmd/pi-pet-daemon`): the Linux X11 app hosts it in-process, the macOS AppKit app supervises it as a bundled child process that exits with the app. It defaults to Unix domain sockets only and creates socket directories with user-only permissions. The standalone daemon uses the same Unix-socket protocol for headless/testing flows.
- No TCP listener is enabled by default. The macOS legacy HTTP state API starts only when `CODEX_PETS_ENABLE_HTTP_STATE_API=1` is set explicitly for manual debugging.
- The Pi extension does not send prompts, provider payloads, tool output, or full approval payload logs to the daemon. It sends lifecycle events and bounded safe summaries such as tool name and a shortened command summary.
- Pet packs are treated as data-only packages. The local importer requires `pet.json` plus a PNG/WebP raster spritesheet, enforces size limits, rejects path traversal for spritesheet paths, and stores license/attribution metadata when present.
- The WebView picker is separate from overlay rendering. The native shell bridge is allowlisted to pet import, installed-pet list, installed-pet select, and Pi extension install/status/uninstall messages.

## Platform Limitations

- The macOS AppKit app supervises the bundled Go daemon as a child process, subscribes to it through the same local protocol path as external clients, and maps daemon attention states to native pet states. The daemon's stdout/stderr land in `~/Library/Application Support/CodexPets/Runtime/pi-pet-daemon.log`. The older local HTTP state API remains as an opt-in debug compatibility path.
- The Linux overlay builds with `sh linux/build.sh` after installing `pkg-config`, `libx11-dev`, `libxext-dev`, GTK 3, and WebKitGTK 4.1 or 4.0 development files (`libX11-devel`, `libXext-devel`, `gtk3-devel`, and `webkit2gtk4.1-devel`/`webkit2gtk4.0-devel` on Fedora) — the X11 backend and Petdex browser need them at build time; the Wayland backend itself is pure Go. On Wayland sessions with layer-shell (Sway, Hyprland, KDE Plasma, wlroots compositors) the pet runs natively with first-class transparency and stacking; on GNOME it falls back to XWayland automatically. The binary also supports `-install-pi-extension`/`-uninstall-pi-extension` for `~/.pi/agent/extensions/codex-pets.ts`. Frame composition (PNG and WebP atlases, animation table, bubble/pill layout, UTF-8 incl. Cyrillic text) lives in `internal/render` and is covered by display-server-free tests; backends are only event loops plus buffer upload.
- The WebView picker can preview Petdex spritesheets, import/select pets, show Pi extension status, and install/uninstall the Pi extension in both the macOS WebKit shell and the Linux WebKitGTK shell. The Linux shell opens from right-clicking the pet body or `-open-petdex`; `-petdex-only` starts just the daemon-backed picker for smoke tests. The local rendered browser smoke covers bridge-driven flows in headless Chrome; the macOS GUI smoke covers the real AppKit app; the Linux GUI smoke is skipped unless Linux, Xvfb, and WebKitGTK development packages are available.
- PadX is represented by `PadXProvider` behind the catalog interface. Web searches for `PadX pet package format API PadX pets manifest`, `PadX provider pet catalog API`, `"PadX" "pets"`, `"PadX" "pet" "manifest"`, and `"PadX" "package" "manifest"` did not reveal a public pet catalog API or package format. To replace the stub, provide a PadX manifest URL, SDK/API docs, or local package format.

Manual standalone daemon smoke test:

```sh
SOCKET=/tmp/pi-pet.sock
go run ./cmd/pi-pet-daemon -socket "$SOCKET"
```

In another shell, run `npm test` for scripted protocol and approval-flow checks. The integration tests prove Pi-extension-style session updates reach a subscribed overlay client, and `pi-extension/index.test.ts` starts the actual daemon, registers the real Pi extension hooks, sends approval responses through the daemon, and verifies the blocked hook resumes.

Manual macOS app smoke test:

1. Build and open `build/CodexPets.app`; the app creates the Pi socket itself.
3. Add `pi-extension/index.ts` to Pi's extension paths and start a Pi session.
4. Confirm the AppKit overlay changes state for `approval_required`, `failed`, `done`, `running`, `thinking`, and `idle`.
5. Trigger a risky Pi bash tool call and confirm the overlay moves into its approval-needed waiting state.
6. In **Browse Petdex...**, use **Install to Pi** to copy the Pi extension, confirm the status changes to installed, use **Uninstall Pi** if you need to reinstall it, then use **Import** for a Petdex pet or **Use** for an installed pet.
# autoupdate test Thu Jun 11 15:19:44 MSK 2026
# update test Thu Jun 11 16:56:50 MSK 2026
# update test 3 Thu Jun 11 17:23:25 MSK 2026
