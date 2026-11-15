//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/duolabmeng6/go-filesystem"
	"golang.org/x/sys/unix"
)

const noReplaceSupported = true

// noReplaceParent opens each component without following symlinks. A directory
// moved after opening remains accessible through its handle.
func (a *Adapter) noReplaceParent(ctx context.Context, path string, create bool) (int, string, error) {
	normalized, err := filesystem.NormalizePath(path)
	if err != nil {
		return -1, "", err
	}
	if normalized == "" {
		return -1, "", fmt.Errorf("%w: empty path", filesystem.ErrInvalidPath)
	}
	parts := strings.Split(normalized, "/")
	fd, err := unix.Open(a.root, noReplaceDirectoryFlags, 0)
	if err != nil {
		return -1, "", mapLocalError(err)
	}
	for _, part := range parts[:len(parts)-1] {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return -1, "", err
		}
		next, openErr := unix.Openat(fd, part, noReplaceDirectoryFlags, 0)
		if create && errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(fd, part, uint32(a.dirMode(a.visibility).Perm()))
			if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
				_ = unix.Close(fd)
				return -1, "", mapLocalError(mkdirErr)
			}
			next, openErr = unix.Openat(fd, part, noReplaceDirectoryFlags, 0)
		}
		if openErr != nil {
			_ = unix.Close(fd)
			return -1, "", mapLocalError(openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func (a *Adapter) copyNoReplace(context.Context, string, string) error {
	return filesystem.ErrUnsupported
}

func (a *Adapter) moveNoReplace(ctx context.Context, src, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := filesystem.NormalizePath(src)
	if err != nil {
		return err
	}
	destination, err := filesystem.NormalizePath(dst)
	if err != nil {
		return err
	}
	if source == "" || destination == "" {
		return fmt.Errorf("%w: empty path", filesystem.ErrInvalidPath)
	}
	if source == destination {
		return filesystem.ErrAlreadyExists
	}
	if strings.HasPrefix(destination, source+"/") {
		return fmt.Errorf("%w: cannot move an entry into itself", filesystem.ErrInvalidPath)
	}
	srcFD, srcName, err := a.noReplaceParent(ctx, src, false)
	if err != nil {
		return err
	}
	defer unix.Close(srcFD)
	var stat unix.Stat_t
	if err := unix.Fstatat(srcFD, srcName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return mapLocalError(err)
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("%w: source is a symlink", filesystem.ErrInvalidPath)
	}
	dstFD, dstName, err := a.noReplaceParent(ctx, dst, true)
	if err != nil {
		return err
	}
	defer unix.Close(dstFD)
	if err := ctx.Err(); err != nil {
		return err
	}
	err = renameNoReplace(srcFD, srcName, dstFD, dstName)
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.ENOTSUP) {
		return fmt.Errorf("%w: %w", filesystem.ErrUnsupported, err)
	}
	return mapLocalError(err)
}
