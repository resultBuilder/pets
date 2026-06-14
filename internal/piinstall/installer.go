package piinstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const InstalledFileName = "codex-pets.ts"

type Options struct {
	SourcePath     string
	DestinationDir string
}

// Status describes the installed/bundled Pi extension pair. It is the
// canonical status payload served over the daemon protocol.
type Status struct {
	Available        bool   `json:"available"`
	Installed        bool   `json:"installed"`
	NeedsUpdate      bool   `json:"needsUpdate"`
	Path             string `json:"path"`
	SourceVersion    string `json:"sourceVersion,omitempty"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Message          string `json:"message,omitempty"`
}

func GetStatus(options Options) Status {
	destinationDir, err := destinationDir(options.DestinationDir)
	if err != nil {
		return Status{Message: err.Error()}
	}
	destinationPath := installedPath(destinationDir)
	installed := isReadableFile(destinationPath)

	source, err := sourcePath(options.SourcePath)
	if err != nil {
		return Status{
			Installed: installed,
			Path:      destinationPath,
			Message:   err.Error(),
		}
	}

	sourceVersion := extensionVersion(source)
	installedVersion := ""
	if installed {
		installedVersion = extensionVersion(destinationPath)
	}
	return Status{
		Available:        true,
		Installed:        installed,
		NeedsUpdate:      installed && isVersionNewer(sourceVersion, installedVersion),
		Path:             destinationPath,
		SourceVersion:    sourceVersion,
		InstalledVersion: installedVersion,
	}
}

// AutoUpdate reinstalls the extension when an older version is installed.
// It returns the installed path and whether an update happened.
func AutoUpdate(options Options) (string, bool, error) {
	status := GetStatus(options)
	if !status.Installed || !status.NeedsUpdate {
		return status.Path, false, nil
	}
	path, err := Install(options)
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}

func Install(options Options) (string, error) {
	sourcePath, err := sourcePath(options.SourcePath)
	if err != nil {
		return "", err
	}
	destinationDir, err := destinationDir(options.DestinationDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return "", err
	}

	destinationPath := installedPath(destinationDir)
	if err := copyFile(sourcePath, destinationPath); err != nil {
		return "", err
	}
	return destinationPath, nil
}

func Uninstall(options Options) (string, error) {
	destinationDir, err := destinationDir(options.DestinationDir)
	if err != nil {
		return "", err
	}
	destinationPath := installedPath(destinationDir)
	if err := os.Remove(destinationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return destinationPath, nil
}

func sourcePath(explicit string) (string, error) {
	if explicit != "" {
		if isReadableFile(explicit) {
			return explicit, nil
		}
		return "", fmt.Errorf("Pi extension source was not found: %s", explicit)
	}

	for _, candidate := range sourceCandidates() {
		if isReadableFile(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("Pi extension source was not found")
}

func sourceCandidates() []string {
	var candidates []string
	if env := os.Getenv("PI_PET_EXTENSION_SOURCE"); env != "" {
		candidates = append(candidates, env)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "pi-extension", "index.ts"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "pi-extension", "index.ts"))
	}
	return candidates
}

func destinationDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent", "extensions"), nil
}

func installedPath(destinationDir string) string {
	return filepath.Join(destinationDir, InstalledFileName)
}

func copyFile(sourcePath string, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	tempPath := destinationPath + ".tmp"
	destination, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(destination, source); err != nil {
		destination.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := destination.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Rename(tempPath, destinationPath)
}

func isReadableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

var versionPattern = regexp.MustCompile(`CODEX_PETS_PI_EXTENSION_VERSION\s*=\s*["']([^"']+)["']`)

func extensionVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	match := versionPattern.FindSubmatch(data)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func isVersionNewer(source string, installed string) bool {
	if source == "" {
		return false
	}
	if installed == "" {
		return true
	}
	return compareVersions(source, installed) > 0
}

func compareVersions(left string, right string) int {
	leftParts := numericVersionComponents(left)
	rightParts := numericVersionComponents(right)
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}
	for i := 0; i < count; i++ {
		leftValue, rightValue := 0, 0
		if i < len(leftParts) {
			leftValue = leftParts[i]
		}
		if i < len(rightParts) {
			rightValue = rightParts[i]
		}
		if leftValue != rightValue {
			if leftValue > rightValue {
				return 1
			}
			return -1
		}
	}
	return 0
}

func numericVersionComponents(version string) []int {
	release, _, _ := strings.Cut(version, "-")
	core, _, _ := strings.Cut(release, "+")
	parts := strings.Split(core, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		value, _ := strconv.Atoi(part)
		out = append(out, value)
	}
	return out
}
