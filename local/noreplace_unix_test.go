//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
)

func TestNoReplaceMoveFileAndDirectory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if disk.Adapter().Capabilities().Has(filesystem.CapabilityCopyNoReplace) {
		t.Fatal("local adapter must not claim unsafe no-replace copy")
	}
	if err := disk.CopyNoReplace(ctx, "source", "target"); !errors.Is(err, filesystem.ErrUnsupported) {
		t.Fatalf("expected unsupported copy, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target"), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := disk.MoveNoReplace(ctx, "source", "target"); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("expected exists, got %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "target"))
	if err != nil || string(got) != "target" {
		t.Fatalf("target = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "source")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "from"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "from", "a"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := disk.MoveNoReplace(ctx, "from", "from/new/target"); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected invalid descendant path, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "from", "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created descendant on rejected move: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "to"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := disk.MoveNoReplace(ctx, "from", "to"); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("expected exists for dir, got %v", err)
	}
	if err := disk.MoveNoReplace(ctx, "from", "moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "moved", "a")); err != nil {
		t.Fatal(err)
	}
}

func TestNoReplaceRejectsSymlinkParents(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []func(context.Context, string, string) error{disk.CopyNoReplace, disk.MoveNoReplace} {
		if err := op(context.Background(), "source", "link/dest"); err == nil {
			t.Fatal("expected symlink error")
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "dest")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside modified: %v", err)
	}
}

func TestNoReplaceMoveWithSearchOnlyDirectories(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"source-dir", "target-dir"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "source-dir", "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"source-dir", "target-dir"} {
		if err := os.Chmod(filepath.Join(root, name), 0300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, name), 0700) })
	}
	if err := disk.MoveNoReplace(context.Background(), "source-dir/file", "target-dir/file"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "target-dir", "file"))
	if err != nil || string(got) != "data" {
		t.Fatalf("target = %q, %v", got, err)
	}
}

func TestNoReplaceConcurrentMove(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			<-start
			results <- disk.MoveNoReplace(context.Background(), src, "target")
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, filesystem.ErrAlreadyExists):
			conflicts++
		default:
			t.Errorf("move: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	target, err := os.ReadFile(filepath.Join(root, "target"))
	if err != nil {
		t.Fatal(err)
	}
	remaining := "first"
	if string(target) == "first" {
		remaining = "second"
	} else if string(target) != "second" {
		t.Fatalf("target %q", target)
	}
	got, err := os.ReadFile(filepath.Join(root, remaining))
	if err != nil || string(got) != remaining {
		t.Fatalf("remaining = %q, %v", got, err)
	}
}
