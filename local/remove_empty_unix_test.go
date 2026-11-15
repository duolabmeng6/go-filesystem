//go:build darwin || linux

package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveEmptyDirectoryRefusesSubstitutedSymlinkParent(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}
	// Models a parent being replaced after the caller's path validation.
	if err := removeEmptyDirectory(context.Background(), root, filepath.Join("parent", "child")); err == nil {
		t.Fatal("expected symlink parent rejection")
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); err != nil {
		t.Fatalf("outside directory changed: %v", err)
	}
}
