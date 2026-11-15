//go:build linux

package local

import "golang.org/x/sys/unix"

const noReplaceDirectoryFlags = unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

func renameNoReplace(srcFD int, src string, dstFD int, dst string) error {
	return unix.Renameat2(srcFD, src, dstFD, dst, unix.RENAME_NOREPLACE)
}
