package gitdriver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
)

func TestLocalRootIncludesGitWorktreeScopeAndReadOnly(t *testing.T) {
	root := t.TempDir()
	if _, err := gogit.PlainInit(root, false); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(context.Background(), Config{URL: "https://example.test/repo.git", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := filesystem.Scoped(filesystem.ReadOnly(adapter), "docs/nested")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := filesystem.LocalRoot(scoped)
	if !ok || got != filepath.Join(adapter.Root(), "docs", "nested") {
		t.Fatalf("scoped worktree root = %q, %v", got, ok)
	}
}
