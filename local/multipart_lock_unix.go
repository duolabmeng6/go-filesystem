//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly || solaris || illumos

package local

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

const multipartLockSupported = true

func tryMultipartLock(file *os.File, exclusive bool) (bool, error) {
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	err := unix.Flock(int(file.Fd()), mode)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}
