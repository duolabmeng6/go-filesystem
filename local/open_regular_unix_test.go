//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	"golang.org/x/sys/unix"
)

func TestOpenRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "pipe")
	if err := unix.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		reader, err := adapter.Open(ctx, "pipe")
		if reader != nil {
			_ = reader.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, filesystem.ErrUnsupported) {
			t.Fatalf("expected non-regular file error, got %v", err)
		}
	case <-time.After(time.Second):
		// Release a blocking implementation before failing so the test does not
		// leak a goroutine, even when exercising the original bug.
		fd, openErr := unix.Open(name, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if openErr == nil {
			defer unix.Close(fd)
		}
		t.Fatal("opening FIFO blocked without a writer")
	}
}

func TestOpenCheckedRegularFileRejectsFIFOReplacement(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "file")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	parent, name, err := adapter.noReplaceParent(context.Background(), "file", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)

	// Place the replacement exactly between the type check and the open.
	var before unix.Stat_t
	if err := unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		t.Fatal("expected a regular file before replacement")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(target, 0o600); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		reader, err := openCheckedRegularFile(context.Background(), parent, name, "file")
		if reader != nil {
			_ = reader.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, filesystem.ErrUnsupported) {
			t.Fatalf("expected non-regular file error, got %v", err)
		}
	case <-time.After(time.Second):
		// Unblock a regressed implementation before closing the parent handle.
		fd, openErr := unix.Open(target, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if openErr == nil {
			defer unix.Close(fd)
			select {
			case <-result:
			case <-time.After(time.Second):
			}
		}
		t.Fatal("opening substituted FIFO blocked without a writer")
	}
}

func TestOpenRejectsSocketAndPreservesRegularFileSeek(t *testing.T) {
	// Unix socket paths have a small platform-specific length limit.
	root, err := os.MkdirTemp("/tmp", "afile-open-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := net.Listen("unix", filepath.Join(root, "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Open(context.Background(), "socket"); !errors.Is(err, filesystem.ErrUnsupported) {
		t.Fatalf("expected non-regular file error, got %v", err)
	}
	reader, err := adapter.Open(context.Background(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	seeker, ok := reader.(io.Seeker)
	if !ok {
		t.Fatal("regular file reader does not support seeking")
	}
	if _, err := seeker.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "llo" {
		t.Fatalf("read %q, error %v", data, err)
	}
}

func TestOpenRegularFileRejectsSubstitutedSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(root, "file")
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), target); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := adapter.openRegularFile(context.Background(), "file", target)
	if reader != nil {
		_ = reader.Close()
	}
	if !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected invalid path, got %v", err)
	}
}
