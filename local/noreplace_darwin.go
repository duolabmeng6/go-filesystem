//go:build darwin

package local

import "golang.org/x/sys/unix"

// Darwin's O_SEARCH is O_EXEC | O_DIRECTORY (sys/fcntl.h).
const noReplaceDirectoryFlags = 0x40000000 | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

func renameNoReplace(srcFD int, src string, dstFD int, dst string) error {
	return unix.RenameatxNp(srcFD, src, dstFD, dst, unix.RENAME_EXCL)
}
