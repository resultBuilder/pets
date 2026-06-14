//go:build !linux

package wayland

import (
	"os"

	"golang.org/x/sys/unix"
)

// newShmFile falls back to an unlinked temp file so the package builds and
// tests run on development hosts without memfd_create.
func newShmFile(size int) (int, error) {
	file, err := os.CreateTemp("", "codex-pets-shm-")
	if err != nil {
		return -1, err
	}
	path := file.Name()
	fd, err := unix.Dup(int(file.Fd()))
	file.Close()
	os.Remove(path)
	if err != nil {
		return -1, err
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
