//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package gitdriver

import "errors"

func gitFilesystemIdentity(string) (string, error) {
	return "", errors.New("filesystem identity unavailable on this platform")
}
