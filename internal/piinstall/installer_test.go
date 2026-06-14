package piinstall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallCopiesAndUpdatesExtension(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "index.ts")
	destinationDir := filepath.Join(root, ".pi", "agent", "extensions")

	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("new extension"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destinationDir, InstalledFileName), []byte("old extension"), 0o644); err != nil {
		t.Fatal(err)
	}

	installedPath, err := Install(Options{SourcePath: sourcePath, DestinationDir: destinationDir})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Base(installedPath) != InstalledFileName {
		t.Fatalf("installed file = %q, want %q", filepath.Base(installedPath), InstalledFileName)
	}
	if string(data) != "new extension" {
		t.Fatalf("installed contents = %q", string(data))
	}
}

func TestInstallReportsMissingExplicitSource(t *testing.T) {
	_, err := Install(Options{
		SourcePath:     filepath.Join(t.TempDir(), "missing.ts"),
		DestinationDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("Install succeeded with a missing source")
	}
}

func TestUninstallRemovesExtension(t *testing.T) {
	destinationDir := t.TempDir()
	destinationPath := filepath.Join(destinationDir, InstalledFileName)
	if err := os.WriteFile(destinationPath, []byte("extension"), 0o644); err != nil {
		t.Fatal(err)
	}

	removedPath, err := Uninstall(Options{DestinationDir: destinationDir})
	if err != nil {
		t.Fatal(err)
	}
	if removedPath != destinationPath {
		t.Fatalf("removed path = %q, want %q", removedPath, destinationPath)
	}
	if _, err := os.Stat(destinationPath); !os.IsNotExist(err) {
		t.Fatalf("installed extension still exists: %v", err)
	}
}

func TestUninstallAllowsAlreadyMissingExtension(t *testing.T) {
	if _, err := Uninstall(Options{DestinationDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func writeExtensionFile(t *testing.T, path string, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "export const CODEX_PETS_PI_EXTENSION_VERSION = \"" + version + "\";\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusReportsVersionsAndUpdateNeed(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "index.ts")
	destinationDir := filepath.Join(root, "extensions")
	options := Options{SourcePath: sourcePath, DestinationDir: destinationDir}

	writeExtensionFile(t, sourcePath, "1.3.0")

	status := GetStatus(options)
	if !status.Available || status.Installed || status.NeedsUpdate {
		t.Fatalf("fresh status = %+v, want available, not installed", status)
	}
	if status.SourceVersion != "1.3.0" {
		t.Fatalf("source version = %q, want 1.3.0", status.SourceVersion)
	}

	writeExtensionFile(t, filepath.Join(destinationDir, InstalledFileName), "1.2.9")
	status = GetStatus(options)
	if !status.Installed || !status.NeedsUpdate {
		t.Fatalf("stale status = %+v, want installed and needsUpdate", status)
	}
	if status.InstalledVersion != "1.2.9" {
		t.Fatalf("installed version = %q, want 1.2.9", status.InstalledVersion)
	}

	writeExtensionFile(t, filepath.Join(destinationDir, InstalledFileName), "1.3.0")
	status = GetStatus(options)
	if status.NeedsUpdate {
		t.Fatalf("current status = %+v, want no update needed", status)
	}
}

func TestStatusWithMissingSourceStillReportsInstall(t *testing.T) {
	destinationDir := t.TempDir()
	writeExtensionFile(t, filepath.Join(destinationDir, InstalledFileName), "1.0.0")

	status := GetStatus(Options{
		SourcePath:     filepath.Join(t.TempDir(), "missing.ts"),
		DestinationDir: destinationDir,
	})
	if status.Available {
		t.Fatalf("status = %+v, want unavailable source", status)
	}
	if !status.Installed {
		t.Fatalf("status = %+v, want installed", status)
	}
	if status.Message == "" {
		t.Fatal("missing source should explain itself in the status message")
	}
}

func TestAutoUpdateReinstallsOnlyStaleVersions(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source", "index.ts")
	destinationDir := filepath.Join(root, "extensions")
	options := Options{SourcePath: sourcePath, DestinationDir: destinationDir}

	writeExtensionFile(t, sourcePath, "2.0.0")

	if _, updated, err := AutoUpdate(options); err != nil || updated {
		t.Fatalf("auto-update with nothing installed = (%v, %v), want no-op", updated, err)
	}

	writeExtensionFile(t, filepath.Join(destinationDir, InstalledFileName), "1.9.9")
	path, updated, err := AutoUpdate(options)
	if err != nil || !updated {
		t.Fatalf("auto-update with stale install = (%v, %v), want update", updated, err)
	}
	if got := extensionVersion(path); got != "2.0.0" {
		t.Fatalf("auto-updated version = %q, want 2.0.0", got)
	}

	if _, updated, err := AutoUpdate(options); err != nil || updated {
		t.Fatalf("auto-update with current install = (%v, %v), want no-op", updated, err)
	}
}

func TestVersionComparisonHandlesPrereleaseAndLengths(t *testing.T) {
	cases := []struct {
		source    string
		installed string
		newer     bool
	}{
		{"1.3.0", "1.2.9", true},
		{"1.3.0", "1.3.0", false},
		{"1.3.0", "1.10.0", false},
		{"1.3.1-beta", "1.3.0", true},
		{"1.3.0+build5", "1.3.0", false},
		{"1.3", "1.3.0", false},
		{"2.0.0", "", true},
		{"", "1.0.0", false},
	}
	for _, tc := range cases {
		if got := isVersionNewer(tc.source, tc.installed); got != tc.newer {
			t.Fatalf("isVersionNewer(%q, %q) = %v, want %v", tc.source, tc.installed, got, tc.newer)
		}
	}
}
