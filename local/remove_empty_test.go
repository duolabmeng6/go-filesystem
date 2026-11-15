//go:build darwin || linux || windows

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
)

func TestDeleteEmptyDirectoryPreservesFilesAndNewChildren(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := disk.MakeDirectory(ctx, "empty"); err != nil {
		t.Fatal(err)
	}
	if err := disk.DeleteEmptyDirectory(ctx, "empty"); err != nil {
		t.Fatalf("remove empty directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "empty")); !os.IsNotExist(err) {
		t.Fatalf("empty directory still exists: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "changed", "new.txt")
	if err := os.WriteFile(child, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := disk.DeleteEmptyDirectory(ctx, "changed"); err == nil {
		t.Fatal("expected non-empty directory deletion to fail")
	}
	if data, err := os.ReadFile(child); err != nil || string(data) != "preserve" {
		t.Fatalf("new child changed: %q, %v", data, err)
	}
	file := filepath.Join(root, "replacement")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := disk.DeleteEmptyDirectory(ctx, "replacement"); err == nil {
		t.Fatal("expected substituted file deletion to fail")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "file" {
		t.Fatalf("replacement file changed: %q, %v", data, err)
	}
	if err := disk.DeleteEmptyDirectory(ctx, ""); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected root rejection, got %v", err)
	}
}

func TestDeleteEmptyDirectoryRespectsScopedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "scope", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	scoped, err := filesystem.Scoped(adapter, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if err := filesystem.NewDisk(filesystem.ReadOnly(scoped)).DeleteEmptyDirectory(context.Background(), "empty"); !errors.Is(err, filesystem.ErrReadOnly) {
		t.Fatalf("expected read-only rejection, got %v", err)
	}
	if err := filesystem.NewDisk(scoped).DeleteEmptyDirectory(context.Background(), "empty"); err != nil {
		t.Fatal(err)
	}
}
