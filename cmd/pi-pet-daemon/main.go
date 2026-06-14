package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"codex-pets/internal/catalog"
	"codex-pets/internal/daemon"
	"codex-pets/internal/petbrain"
	"codex-pets/internal/piinstall"
	"codex-pets/internal/protocol"
	"codex-pets/internal/updater"
)

// defaultDialogueHistoryPath keeps murmur history next to the daemon state.
func defaultDialogueHistoryPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "codex-pets", "dialogue-history.json")
}

// configureUpdater wires the shared self-update pipeline when the daemon
// runs from a git checkout and the host opted in with a build script.
func configureUpdater(server *daemon.Server, store *daemon.Store, repoRoot string, buildScript string) {
	if buildScript == "" {
		return
	}
	if repoRoot == "" {
		if executable, err := os.Executable(); err == nil {
			repoRoot = updater.DetectRepoRoot(executable)
		}
	}
	if repoRoot == "" {
		fmt.Fprintln(os.Stderr, "pi-pet-daemon: self-update disabled: no git checkout found")
		return
	}
	server.Updater = updater.New(repoRoot, buildScript, func(state protocol.UpdateState) {
		server.PublishSnapshot(store.SetUpdateState(state))
	})
}

func main() {
	socketPath := flag.String("socket", daemon.DefaultSocketPath(), "Unix domain socket path")
	stateFile := flag.String("state-file", daemon.DefaultStateFilePath(), "file for persisted daemon state such as the selected pet (empty disables persistence)")
	watchStdin := flag.Bool("watch-stdin", false, "exit when stdin reaches EOF (set by a supervising parent app)")
	piExtensionSource := flag.String("pi-extension-source", "", "path to the bundled Pi extension served by pi.extension.* methods")
	piExtensionAutoUpdate := flag.Bool("pi-extension-autoupdate", false, "reinstall the Pi extension at startup when an older version is installed")
	repoRoot := flag.String("repo-root", "", "git checkout for self-updates (empty autodetects from the daemon binary location)")
	buildScript := flag.String("build-script", "", "host build script for update.apply, relative to the repo root (empty disables self-updates)")
	var petSources []catalog.InstalledRoot
	flag.Func("pets-root", "pet packages directory as source:path (repeatable); the daemon scans these into the snapshot", func(value string) error {
		source, dir, ok := strings.Cut(value, ":")
		if !ok || source == "" || dir == "" {
			return fmt.Errorf("expected source:path, got %q", value)
		}
		petSources = append(petSources, catalog.InstalledRoot{Dir: dir, Source: source})
		return nil
	})
	petImportRoot := flag.String("pets-import-root", "", "directory that receives pet.import packages (scanned with the petdex source)")
	catalogDir := flag.String("catalog-dir", "", "bundled pet catalog directory (with pets/<slug> packages) offered in pets.browser.list")
	dialogueHistory := flag.String("dialogue-history", defaultDialogueHistoryPath(), "file persisting murmur history (empty disables persistence)")
	flag.Parse()

	if *piExtensionAutoUpdate {
		options := piinstall.Options{SourcePath: *piExtensionSource}
		if path, updated, err := piinstall.AutoUpdate(options); err != nil {
			fmt.Fprintf(os.Stderr, "pi-pet-daemon: pi extension auto-update: %v\n", err)
		} else if updated {
			fmt.Printf("pi-pet-daemon: updated Pi extension at %s\n", path)
		}
	}

	listener, err := daemon.ListenUnix(*socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pi-pet-daemon: listen: %v\n", err)
		os.Exit(1)
	}
	defer os.Remove(*socketPath)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *watchStdin {
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	}

	fmt.Printf("pi-pet-daemon listening on %s\n", *socketPath)
	store := daemon.NewStoreWithStateFile(*stateFile)
	server := daemon.NewServer(store)
	server.PiExtensionSource = *piExtensionSource
	configureUpdater(server, store, *repoRoot, *buildScript)
	if *petImportRoot != "" {
		server.PetImportRoot = *petImportRoot
		alreadyScanned := false
		for _, root := range petSources {
			if root.Dir == *petImportRoot {
				alreadyScanned = true
				break
			}
		}
		if !alreadyScanned {
			petSources = append(petSources, catalog.InstalledRoot{Dir: *petImportRoot, Source: "petdex"})
		}
	}
	server.PetSources = petSources
	server.CatalogDir = *catalogDir
	server.RefreshInstalledPets()
	store.AttachBrain(petbrain.NewWithHistoryFile(*dialogueHistory), server.PublishSnapshot)
	if err := server.Serve(ctx, listener); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, net.ErrClosed) {
		fmt.Fprintf(os.Stderr, "pi-pet-daemon: %v\n", err)
		os.Exit(1)
	}
}
