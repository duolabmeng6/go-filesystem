//go:build windows

package local

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/duolabmeng6/go-filesystem"
	"golang.org/x/sys/windows"
)

// A relative directory handle prevents path replacement from redirecting the
// operation. FILE_DIRECTORY_FILE and the disposition request remove only an
// empty directory, never a file or the contents of a concurrently populated one.
func removeEmptyDirectory(ctx context.Context, root, relative string) error {
	rootPath, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return err
	}
	current, err := windows.CreateFile(rootPath, windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(current) }()
	if err := rejectWindowsReparsePoint(current); err != nil {
		return err
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	for index, part := range parts {
		if err := ctx.Err(); err != nil {
			return err
		}
		name, err := windows.NewNTUnicodeString(part)
		if err != nil {
			return err
		}
		attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: current, ObjectName: name,
			Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
		attrs.Length = uint32(unsafe.Sizeof(attrs))
		access := uint32(windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE)
		if index == len(parts)-1 {
			access |= windows.DELETE
		}
		var next windows.Handle
		var status windows.IO_STATUS_BLOCK
		err = windows.NtCreateFile(&next, access, &attrs, &status, nil, 0,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
		if err != nil {
			var ntStatus windows.NTStatus
			if errors.As(err, &ntStatus) {
				return ntStatus.Errno()
			}
			return err
		}
		if err := rejectWindowsReparsePoint(next); err != nil {
			_ = windows.CloseHandle(next)
			return err
		}
		_ = windows.CloseHandle(current)
		current = next
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(current, windows.FileDispositionInfo, &deleteFile, 1)
}

func rejectWindowsReparsePoint(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: symbolic links are not supported", filesystem.ErrInvalidPath)
	}
	return nil
}
