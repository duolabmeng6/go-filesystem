//go:build darwin || linux

package local

import (
	"context"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// Open each parent without following symlinks and use rmdir semantics. A
// concurrent replacement with a file or a new child therefore cannot be removed.
func removeEmptyDirectory(ctx context.Context, root, relative string) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Unlinkat(fd, parts[len(parts)-1], unix.AT_REMOVEDIR)
}
