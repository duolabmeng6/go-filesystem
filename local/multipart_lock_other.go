//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !solaris && !illumos && !windows

package local

import (
	"os"

	"github.com/duolabmeng6/go-filesystem"
)

const multipartLockSupported = false

func tryMultipartLock(_ *os.File, _ bool) (bool, error) {
	return false, filesystem.ErrUnsupported
}
