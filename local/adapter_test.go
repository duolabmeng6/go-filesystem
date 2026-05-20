package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
)

func newTestDisk(t testing.TB) *filesystem.Disk {
	t.Helper()
	disk, err := NewDisk(Config{Root: t.TempDir(), BaseURL: "/storage"})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	return disk
}

func TestContracts(t *testing.T) {
	filesystemtest.RunObjectContract(t, newTestDisk)
	filesystemtest.RunDirectoryContract(t, newTestDisk)
	filesystemtest.RunListContract(t, newTestDisk)
	filesystemtest.RunVisibilityContract(t, newTestDisk)
	filesystemtest.RunPathSafetyContract(t, newTestDisk)
	filesystemtest.RunURLContract(t, newTestDisk)
	filesystemtest.RunTemporaryURLContract(t, newTestDisk)
}

func TestTemporaryURLBuilder(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	disk, err := NewDisk(Config{
		Root: t.TempDir(),
		TemporaryURLBuilder: func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
			if path != "a.txt" {
				t.Fatalf("path=%q", path)
			}
			if !expiresAt.Equal(expires) {
				t.Fatalf("expiresAt=%v", expiresAt)
			}
			return "/tmp/a.txt?token=1", nil
		},
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	got, err := disk.TemporaryURL(context.Background(), "a.txt", expires)
	if err != nil {
		t.Fatalf("temporary url: %v", err)
	}
	if got != "/tmp/a.txt?token=1" {
		t.Fatalf("got %q", got)
	}
}

func TestRootBaseURL(t *testing.T) {
	disk, err := NewDisk(Config{Root: t.TempDir(), BaseURL: "/"})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	got, err := disk.URL(context.Background(), "a b.txt")
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	if got != "/a%20b.txt" {
		t.Fatalf("got %q", got)
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	_, err = disk.Get(context.Background(), "link/secret.txt")
	if !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected invalid path, got %v", err)
	}
	err = disk.Put(context.Background(), "link/write.txt", []byte("x"))
	if !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected invalid path on write, got %v", err)
	}
}

func TestAtomicWriteCleansUpTempOnReaderFailure(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	err = disk.Write(context.Background(), "a.txt", failingReader{})
	if err == nil {
		t.Fatalf("expected write error")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temp file was not cleaned up: %s", entry.Name())
		}
	}
}

func TestOverwriteFalse(t *testing.T) {
	disk := newTestDisk(t)
	ctx := context.Background()
	if err := disk.Put(ctx, "a.txt", []byte("a")); err != nil {
		t.Fatalf("put: %v", err)
	}
	err := disk.Put(ctx, "a.txt", []byte("b"), filesystem.WithOverwrite(false))
	if !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("expected already exists, got %v", err)
	}
	got, err := disk.Get(ctx, "a.txt")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "a" {
		t.Fatalf("content changed to %q", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("reader failed")
}
