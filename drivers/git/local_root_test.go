package gitdriver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
)

func TestLocalRootIncludesGitWorktreeScopeAndReadOnly(t *testing.T) {
	root := t.TempDir()
	repo, err := gogit.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: defaultRemoteName, URLs: []string{"https://example.test/repo.git"}}); err != nil {
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
