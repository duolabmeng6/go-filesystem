//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package gitdriver

import (
	"fmt"
	"os"
	"syscall"
)

func gitFilesystemIdentity(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(info, current) {
		return "", fmt.Errorf("filesystem identity changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return "", fmt.Errorf("filesystem identity unavailable")
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
