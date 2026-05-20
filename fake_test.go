package filesystem_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/local"
)

func TestFakeReplacesAndRestoresDisk(t *testing.T) {
	_ = local.Config{}
	manager := filesystem.New(filesystem.WithDefaultDisk("local"))
	original, err := local.NewDisk(local.Config{Root: t.TempDir()})
	if err != nil {
		t.Fatalf("original: %v", err)
	}
	if err := manager.RegisterDisk("local", original); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := manager.Put(context.Background(), "original.txt", []byte("original")); err != nil {
		t.Fatalf("put original: %v", err)
	}
	t.Run("fake scope", func(t *testing.T) {
		fake := filesystem.Fake(t, manager, "local")
		if err := manager.Put(context.Background(), "a.txt", []byte("fake")); err != nil {
			t.Fatalf("put fake: %v", err)
		}
		fake.AssertExists("a.txt")
		fake.AssertContent("a.txt", []byte("fake"))
	})
	got, err := manager.Get(context.Background(), "original.txt")
	if err != nil {
		t.Fatalf("get original after restore: %v", err)
	}
	if string(got) != "original" {
		t.Fatalf("got %q", got)
	}
	exists, err := manager.Exists(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("exists fake path after restore: %v", err)
	}
	if exists {
		t.Fatalf("fake file leaked into restored disk")
	}
}

func TestPersistentFakeKeepsFiles(t *testing.T) {
	root := t.TempDir()
	manager := filesystem.New(filesystem.WithDefaultDisk("local"))
	fake, err := filesystem.PersistentFake(manager, "local", root)
	if err != nil {
		t.Fatalf("persistent fake: %v", err)
	}
	if err := fake.Put(context.Background(), "a.txt", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("expected persistent file: %v", err)
	}
}
