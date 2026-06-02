package gitdriver

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestContracts(t *testing.T) {
	filesystemtest.RunObjectContract(t, newTestDisk)
	filesystemtest.RunDirectoryContract(t, newTestDisk)
	filesystemtest.RunListContract(t, newTestDisk)
	filesystemtest.RunPathSafetyContract(t, newTestDisk)
}

func TestReadOnlyContract(t *testing.T) {
	filesystemtest.RunReadOnlyContract(t, func(t testing.TB) *filesystem.Disk {
		t.Helper()
		disk, err := NewDisk(context.Background(), Config{
			URL:      seedRepository(t),
			Root:     t.TempDir(),
			ReadOnly: true,
		})
		if err != nil {
			t.Fatalf("new disk: %v", err)
		}
		return disk
	})
}

func TestFactoryUsesOptions(t *testing.T) {
	manager := filesystem.New(filesystem.WithDriver("git", NewFactory()))
	disk, err := manager.Build(context.Background(), filesystem.DiskConfig{
		Driver: "git",
		Root:   t.TempDir(),
		Options: map[string]any{
			"url":         seedRepository(t),
			"branch":      "master",
			"auth_mode":   "none",
			"auto_pull":   true,
			"commit_name": "Tester",
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if disk == nil {
		t.Fatalf("expected disk")
	}
}

func TestStatusAndCommit(t *testing.T) {
	disk := newTestDisk(t)
	adapter, ok := disk.Adapter().(*Adapter)
	if !ok {
		t.Fatalf("expected git adapter, got %#v", disk.Adapter())
	}
	if err := disk.Put(context.Background(), "docs/a.txt", []byte("alpha")); err != nil {
		t.Fatalf("put: %v", err)
	}
	status, err := adapter.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Dirty || len(status.Changed) != 1 || status.Changed[0] != "docs/a.txt" {
		t.Fatalf("unexpected status: %#v", status)
	}
	hash, err := adapter.Commit(context.Background(), "同步文件变更")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if hash == "" {
		t.Fatalf("expected commit hash")
	}
	status, err = adapter.Status(context.Background())
	if err != nil {
		t.Fatalf("status clean: %v", err)
	}
	if status.Dirty {
		t.Fatalf("expected clean status after commit: %#v", status)
	}
}

func TestGitInternalPathRejected(t *testing.T) {
	disk := newTestDisk(t)
	err := disk.Put(context.Background(), ".git/config", []byte("bad"))
	if !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected invalid path, got %v", err)
	}
}

func TestGitIgnorePreventsUnsyncableWrite(t *testing.T) {
	disk := newTestDisk(t)
	if err := disk.Put(context.Background(), ".gitignore", []byte("*.tmp\n")); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if _, err := adapter.Commit(context.Background(), "add ignore"); err != nil {
		t.Fatalf("commit gitignore: %v", err)
	}
	err := disk.Put(context.Background(), "cache.tmp", []byte("ignored"))
	if !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("expected ignored write to be invalid, got %v", err)
	}
}

func TestSyncPushesDirtyWorktree(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{
		URL:  remoteURL,
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "docs/a.txt", []byte("local")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := adapter.Sync(context.Background(), "sync local"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	clone := cloneRepository(t, remoteURL)
	content, err := os.ReadFile(filepath.Join(clone, "docs/a.txt"))
	if err != nil {
		t.Fatalf("read pushed file: %v", err)
	}
	if string(content) != "local" {
		t.Fatalf("unexpected pushed content: %q", string(content))
	}
}

func TestEmptyRepositoryCanMountAndPushFirstCommit(t *testing.T) {
	remoteURL := emptyBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{
		URL:    remoteURL,
		Root:   t.TempDir(),
		Branch: "main",
	})
	if err != nil {
		t.Fatalf("new empty disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "README.md", []byte("hello")); err != nil {
		t.Fatalf("put first file: %v", err)
	}
	if err := adapter.Sync(context.Background(), "first sync"); err != nil {
		t.Fatalf("sync empty repo: %v", err)
	}

	clone := cloneRepositoryBranch(t, remoteURL, "main")
	content, err := os.ReadFile(filepath.Join(clone, "README.md"))
	if err != nil {
		t.Fatalf("read first pushed file: %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("unexpected first pushed content: %q", string(content))
	}
}

func TestSyncPreservesRemoteConflictCopy(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{
		URL:  remoteURL,
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "docs/a.txt", []byte("local")); err != nil {
		t.Fatalf("put local change: %v", err)
	}
	updateRemoteFile(t, remoteURL, "docs/a.txt", "remote")

	if err := adapter.Sync(context.Background(), "sync conflict"); err != nil {
		t.Fatalf("sync conflict: %v", err)
	}
	localContent, err := disk.Get(context.Background(), "docs/a.txt")
	if err != nil {
		t.Fatalf("read local file: %v", err)
	}
	if string(localContent) != "local" {
		t.Fatalf("expected local version to keep original name, got %q", string(localContent))
	}
	page, err := disk.ListPage(context.Background(), "docs")
	if err != nil {
		t.Fatalf("list docs: %v", err)
	}
	conflictPath := ""
	for _, item := range page.Items {
		if item.Path != "docs/a.txt" {
			conflictPath = item.Path
			break
		}
	}
	if conflictPath == "" {
		t.Fatalf("expected conflict copy, got %#v", page.Items)
	}
	remoteCopy, err := disk.Get(context.Background(), conflictPath)
	if err != nil {
		t.Fatalf("read conflict copy: %v", err)
	}
	if string(remoteCopy) != "remote" {
		t.Fatalf("expected remote version in conflict copy, got %q", string(remoteCopy))
	}
	conflicts, err := adapter.Conflicts(context.Background())
	if err != nil {
		t.Fatalf("list conflicts: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != conflictPath {
		t.Fatalf("unexpected conflicts: %#v", conflicts)
	}
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		raw      string
		proto    string
		platform string
		path     string
		repo     string
	}{
		{"https://github.com/owner/repo.git", "https", "github.com", "owner/repo", "repo"},
		{"git@github.com:owner/repo.git", "ssh", "github.com", "owner/repo", "repo"},
		{"ssh://git@github.com/owner/repo.git", "ssh", "github.com", "owner/repo", "repo"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := ParseURL(tt.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Protocol != tt.proto || got.Platform != tt.platform || got.RepositoryPath != tt.path || got.Repo != tt.repo {
				t.Fatalf("unexpected parse result: %#v", got)
			}
		})
	}
}

func TestConflictFileName(t *testing.T) {
	at := time.Date(2026, 6, 2, 21, 30, 0, 0, time.Local)
	if got := ConflictFileName("docs/笔记.md", at); got != "docs/笔记（冲突文件-20260602-2130）.md" {
		t.Fatalf("unexpected conflict name: %q", got)
	}
	if got := ConflictFileName("README", at); got != "README（冲突文件-20260602-2130）" {
		t.Fatalf("unexpected extensionless conflict name: %q", got)
	}
}

func TestConnectionReadOnly(t *testing.T) {
	result, err := TestConnection(context.Background(), Config{
		URL:      seedRepository(t),
		AuthMode: AuthModeNone,
	})
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if !result.CanRead || result.CanWrite || result.Mode != AccessModeReadOnly {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestConnectionWritableProbe(t *testing.T) {
	result, err := TestConnection(context.Background(), Config{
		URL:      seedRepository(t),
		AuthMode: AuthModePassword,
		Username: "git",
		Password: "token",
	})
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if !result.CanRead || !result.CanWrite || result.Mode != AccessModeReadWrite {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func newTestDisk(t testing.TB) *filesystem.Disk {
	t.Helper()
	disk, err := NewDisk(context.Background(), Config{
		URL:  seedRepository(t),
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	return disk
}

func seedRepository(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	repo, err := gogit.PlainInit(root, false)
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	_, err = worktree.Commit("initial", &gogit.CommitOptions{
		AllowEmptyCommits: true,
		Author: &object.Signature{
			Name:  "Tester",
			Email: "tester@example.local",
			When:  time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return (&url.URL{Scheme: "file", Path: root}).String()
}

func seedBareRepository(t testing.TB) string {
	t.Helper()
	remoteURL := emptyBareRepository(t)
	work := t.TempDir()
	repo, err := gogit.PlainInit(work, false)
	if err != nil {
		t.Fatalf("init seed worktree: %v", err)
	}
	if err := setHeadBranch(repo, "master"); err != nil {
		t.Fatalf("set seed head: %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: defaultRemoteName, URLs: []string{remoteURL}}); err != nil {
		t.Fatalf("create seed remote: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(work, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir seed docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "docs/a.txt"), []byte("base"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	commitAndPush(t, repo, "initial")
	return remoteURL
}

func emptyBareRepository(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	if _, err := gogit.PlainInit(root, true); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}
	return (&url.URL{Scheme: "file", Path: root}).String()
}

func cloneRepository(t testing.TB, remoteURL string) string {
	t.Helper()
	return cloneRepositoryBranch(t, remoteURL, "")
}

func cloneRepositoryBranch(t testing.TB, remoteURL string, branch string) string {
	t.Helper()
	root := t.TempDir()
	options := &gogit.CloneOptions{URL: remoteURL}
	if branch != "" {
		options.ReferenceName = branchReferenceName(branch)
		options.SingleBranch = true
	}
	if _, err := gogit.PlainClone(root, false, options); err != nil {
		t.Fatalf("clone repo: %v", err)
	}
	return root
}

func updateRemoteFile(t testing.TB, remoteURL string, filePath string, content string) {
	t.Helper()
	root := cloneRepository(t, remoteURL)
	repo, err := gogit.PlainOpen(root)
	if err != nil {
		t.Fatalf("open remote update clone: %v", err)
	}
	target := filepath.Join(root, filepath.FromSlash(filePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir remote update: %v", err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatalf("write remote update: %v", err)
	}
	commitAndPush(t, repo, "remote update")
}

func commitAndPush(t testing.TB, repo *gogit.Repository, message string) {
	t.Helper()
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := worktree.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := worktree.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  "Tester",
			Email: "tester@example.local",
			When:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&gogit.PushOptions{RemoteName: defaultRemoteName}); err != nil {
		t.Fatalf("push: %v", err)
	}
}
