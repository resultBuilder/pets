package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"codex-pets/internal/catalog"
	"codex-pets/internal/daemon"
	"codex-pets/internal/petbrain"
	"codex-pets/internal/piinstall"
	"codex-pets/internal/protocol"
	"codex-pets/internal/updater"
	"codex-pets/internal/overlayhost"
	"codex-pets/internal/waylandoverlay"
	"codex-pets/internal/x11overlay"
)

// defaultDialogueHistoryPath keeps murmur history next to the daemon state.
func defaultDialogueHistoryPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "codex-pets", "dialogue-history.json")
}

func main() {
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "Pi Pet daemon Unix socket path")
	stateFile := flag.String("state-file", daemon.DefaultStateFilePath(), "file for persisted daemon state such as the selected pet (empty disables persistence)")
	scale := flag.Float64("scale", 0, "overlay UI scale factor (0 autodetects)")
	backendName := flag.String("backend", "auto", "windowing backend: auto, wayland, or x11")
	petsDir := flag.String("pets-dir", "", "extra directory with pet packages to offer alongside the prebundled ones")
	installPiExtension := flag.Bool("install-pi-extension", false, "Install the Pi extension into ~/.pi/agent/extensions and exit")
	uninstallPiExtension := flag.Bool("uninstall-pi-extension", false, "Uninstall the Pi extension from ~/.pi/agent/extensions and exit")
	piExtensionSource := flag.String("pi-extension-source", "", "Pi extension source file for -install-pi-extension")
	piExtensionDir := flag.String("pi-extension-dir", "", "Pi extension install directory for -install-pi-extension or -uninstall-pi-extension")
	buildScript := flag.String("build-script", "linux/build.sh", "host build script for update.apply, relative to the repo root (empty disables self-updates)")
	flag.Parse()

	if *installPiExtension && *uninstallPiExtension {
		fmt.Fprintln(os.Stderr, "pi-pet-overlay: choose only one of -install-pi-extension or -uninstall-pi-extension")
		os.Exit(2)
	}

	if *installPiExtension {
		installedPath, err := piinstall.Install(piinstall.Options{
			SourcePath:     *piExtensionSource,
			DestinationDir: *piExtensionDir,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: install Pi extension: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Installed Pi extension to %s\n", installedPath)
		return
	}

	if *uninstallPiExtension {
		removedPath, err := piinstall.Uninstall(piinstall.Options{
			DestinationDir: *piExtensionDir,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: uninstall Pi extension: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Uninstalled Pi extension from %s\n", removedPath)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listener, err := daemon.ListenUnix(*socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pi-pet-overlay: daemon listen: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(*socketPath)

	repoRoot := detectRepoRoot()
	store := daemon.NewStoreWithStateFile(*stateFile)
	server := daemon.NewServer(store)
	server.PetSources = installedPetRoots(repoRoot, *petsDir)
	if home, err := os.UserHomeDir(); err == nil {
		server.PetImportRoot = filepath.Join(home, ".petdex", "pets")
	}
	if snapshot := server.RefreshInstalledPets(); len(snapshot.InstalledPets) == 0 {
		fmt.Fprintln(os.Stderr, "pi-pet-overlay: no pet packages found; the overlay will show a placeholder")
	}
	store.AttachBrain(petbrain.NewWithHistoryFile(defaultDialogueHistoryPath()), server.PublishSnapshot)
	if *buildScript != "" && repoRoot != "" {
		server.Updater = updater.New(repoRoot, *buildScript, func(state protocol.UpdateState) {
			server.PublishSnapshot(store.SetUpdateState(state))
		})
	}

	daemonErr := make(chan error, 1)
	go func() {
		daemonErr <- server.Serve(ctx, listener)
	}()

	backend, backendLabel, err := pickBackend(*backendName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pi-pet-overlay: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("pi-pet-overlay: using %s backend\n", backendLabel)

	overlayErr := make(chan error, 1)
	go func() {
		overlayErr <- overlayhost.Run(ctx, *socketPath, backend, *scale)
	}()

	select {
	case err := <-overlayErr:
		stop()
		if !isExpectedShutdown(err) {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: %v\n", err)
			os.Exit(1)
		}
	case err := <-daemonErr:
		stop()
		if !isExpectedShutdown(err) {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: daemon: %v\n", err)
			os.Exit(1)
		}
	}
}

// detectRepoRoot finds the git checkout from the binary location, falling
// back to the working directory for `go run` development sessions.
func detectRepoRoot() string {
	if executable, err := os.Executable(); err == nil {
		if root := updater.DetectRepoRoot(executable); root != "" {
			return root
		}
	}
	if cwd, err := os.Getwd(); err == nil {
		// DetectRepoRoot starts from the parent, so anchor one level deeper
		// to include cwd itself.
		if root := updater.DetectRepoRoot(filepath.Join(cwd, "x")); root != "" {
			return root
		}
	}
	return ""
}

// installedPetRoots mirrors the macOS app's pet sources: prebundled packages
// from the checkout plus user-imported pets in the home directory.
func installedPetRoots(repoRoot string, extraDir string) []catalog.InstalledRoot {
	var roots []catalog.InstalledRoot
	if repoRoot != "" {
		roots = append(roots, catalog.InstalledRoot{Dir: filepath.Join(repoRoot, "prebundled-pets", "pets"), Source: "app"})
	}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			catalog.InstalledRoot{Dir: filepath.Join(home, ".codex", "pets"), Source: "codex"},
			catalog.InstalledRoot{Dir: filepath.Join(home, ".petdex", "pets"), Source: "petdex"},
		)
	}
	if extraDir != "" {
		roots = append(roots, catalog.InstalledRoot{Dir: extraDir, Source: "app"})
	}
	return roots
}

// pickBackend prefers native Wayland (first-class transparency and
// always-on-top via layer-shell) and falls back to X11/XWayland — e.g. on
// GNOME, whose compositor does not implement wlr-layer-shell.
func pickBackend(name string) (overlayhost.Backend, string, error) {
	switch name {
	case "wayland":
		backend, err := waylandoverlay.New()
		return backend, "wayland", err
	case "x11":
		backend, err := x11overlay.New()
		return backend, "x11", err
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		if backend, err := waylandoverlay.New(); err == nil {
			return backend, "wayland", nil
		} else {
			fmt.Fprintf(os.Stderr, "pi-pet-overlay: wayland unavailable (%v); falling back to X11\n", err)
		}
	}
	backend, err := x11overlay.New()
	return backend, "x11 (set WAYLAND_DISPLAY for native wayland)", err
}

func isExpectedShutdown(err error) bool {
	return err == nil || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed)
}
