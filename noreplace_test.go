package filesystem

import (
	"context"
	"errors"
	"testing"
)

type noReplaceTestAdapter struct {
	*memoryAdapter
	source, destination, action string
}

func (a *noReplaceTestAdapter) Capabilities() CapabilitySet {
	return NewCapabilitySet(CapabilityCopyNoReplace, CapabilityMoveNoReplace)
}
func (a *noReplaceTestAdapter) CopyNoReplace(_ context.Context, src, dst string) error {
	a.source, a.destination, a.action = src, dst, "copy"
	return nil
}
func (a *noReplaceTestAdapter) MoveNoReplace(_ context.Context, src, dst string) error {
	a.source, a.destination, a.action = src, dst, "move"
	return nil
}

func TestNoReplaceScopedAndReadOnly(t *testing.T) {
	base := &noReplaceTestAdapter{memoryAdapter: newMemoryAdapter()}
	scoped, err := Scoped(base, "scope")
	if err != nil {
		t.Fatal(err)
	}
	disk := NewDisk(scoped)
	if err := disk.CopyNoReplace(context.Background(), "a", "b"); err != nil {
		t.Fatal(err)
	}
	if base.source != "scope/a" || base.destination != "scope/b" || base.action != "copy" {
		t.Fatalf("unexpected call: %+v", base)
	}
	if err := disk.MoveNoReplace(context.Background(), "a", "b"); err != nil {
		t.Fatal(err)
	}
	if base.action != "move" {
		t.Fatalf("action = %q", base.action)
	}
	readonly := NewDisk(ReadOnly(scoped))
	if err := readonly.CopyNoReplace(context.Background(), "a", "b"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("copy: %v", err)
	}
	if err := readonly.MoveNoReplace(context.Background(), "a", "b"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("move: %v", err)
	}
	if err := disk.CopyNoReplace(context.Background(), "../outside", "b"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("path: %v", err)
	}
	unsupported := NewDisk(newMemoryAdapter())
	if err := unsupported.CopyNoReplace(context.Background(), "a", "b"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported: %v", err)
	}
}
