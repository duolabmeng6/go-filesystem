package gitdriver

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
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
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &identity); err != nil {
		return "", err
	}
	if identity.FileIndexHigh == 0 && identity.FileIndexLow == 0 {
		return "", fmt.Errorf("filesystem identity unavailable")
	}
	return fmt.Sprintf("%x:%x:%x", identity.VolumeSerialNumber, identity.FileIndexHigh, identity.FileIndexLow), nil
}
