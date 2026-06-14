//go:build linux

package wayland

import "golang.org/x/sys/unix"

// newShmFile returns a sealed anonymous file for wl_shm pools.
func newShmFile(size int) (int, error) {
	fd, err := unix.MemfdCreate("codex-pets-shm", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return -1, err
	}
	if err := unix.Ftruncate(fd, int64(size)); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}
