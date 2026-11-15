//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/duolabmeng6/go-filesystem"
	"golang.org/x/sys/unix"
)

// Open relative to no-follow directory handles, and use O_NONBLOCK so a FIFO
// substituted after the type check cannot block the calling goroutine.
func (a *Adapter) openRegularFile(ctx context.Context, path, _ string) (io.ReadCloser, error) {
	parent, name, err := a.noReplaceParent(ctx, path, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)

	var before unix.Stat_t
	if err := unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, mapLocalError(err)
	}
	if before.Mode&unix.S_IFMT == unix.S_IFLNK {
		return nil, fmt.Errorf("%w: symlink target is not allowed", filesystem.ErrInvalidPath)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("%w: cannot open non-regular file", filesystem.ErrUnsupported)
	}
	return openCheckedRegularFile(ctx, parent, name, path)
}

// Recheck the opened handle: the directory entry can change after Fstatat.
func openCheckedRegularFile(ctx context.Context, parent int, name, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%w: symlink target is not allowed", filesystem.ErrInvalidPath)
		}
		return nil, mapLocalError(err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%w: cannot open file", filesystem.ErrUnsupported)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, mapLocalError(err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: cannot open non-regular file", filesystem.ErrUnsupported)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
