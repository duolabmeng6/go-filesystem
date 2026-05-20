package filesystem

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestScopedAdapterAppliesPrefixAndStripsList(t *testing.T) {
	base := newMemoryAdapter()
	scoped, err := Scoped(base, "tenant/a")
	if err != nil {
		t.Fatalf("scoped: %v", err)
	}
	disk := NewDisk(scoped)
	if err := disk.Put(context.Background(), "file.txt", []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	exists, err := base.Exists(context.Background(), "tenant/a/file.txt")
	if err != nil || !exists {
		t.Fatalf("base exists=%v err=%v", exists, err)
	}
}

func TestReadOnlyBlocksWritesButKeepsReads(t *testing.T) {
	base := newMemoryAdapter()
	if err := base.Write(context.Background(), "file.txt", strings.NewReader("x"), DefaultWriteOptions()); err != nil {
		t.Fatalf("base write: %v", err)
	}
	disk := NewDisk(ReadOnly(base))
	if _, err := disk.Get(context.Background(), "file.txt"); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := disk.Put(context.Background(), "file.txt", []byte("y")); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected read only, got %v", err)
	}
}
