package gitdriver

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
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

func TestCommitGeneratesDefaultMessageForSingleFileActions(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, disk *filesystem.Disk, adapter *Adapter)
		want  string
	}{
		{
			name: "added",
			setup: func(t *testing.T, disk *filesystem.Disk, adapter *Adapter) {
				t.Helper()
				if err := disk.Put(context.Background(), "docs/new.md", []byte("new")); err != nil {
					t.Fatalf("put new file: %v", err)
				}
			},
			want: "新增 docs/new.md",
		},
		{
			name: "modified",
			setup: func(t *testing.T, disk *filesystem.Disk, adapter *Adapter) {
				t.Helper()
				seedTrackedFiles(t, disk, adapter, map[string]string{"docs/note.md": "base"})
				if err := disk.Put(context.Background(), "docs/note.md", []byte("changed")); err != nil {
					t.Fatalf("update file: %v", err)
				}
			},
			want: "更新 docs/note.md",
		},
		{
			name: "deleted",
			setup: func(t *testing.T, disk *filesystem.Disk, adapter *Adapter) {
				t.Helper()
				seedTrackedFiles(t, disk, adapter, map[string]string{"docs/remove.md": "base"})
				if err := disk.Delete(context.Background(), "docs/remove.md"); err != nil {
					t.Fatalf("delete file: %v", err)
				}
			},
			want: "删除 docs/remove.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disk := newTestDisk(t)
			adapter := disk.Adapter().(*Adapter)
			tt.setup(t, disk, adapter)
			hash, err := adapter.Commit(context.Background(), "")
			if err != nil {
				t.Fatalf("commit: %v", err)
			}
			if hash == "" {
				t.Fatalf("expected commit hash")
			}
			if got := lastCommitMessage(t, adapter); got != tt.want {
				t.Fatalf("unexpected commit message:\nwant %q\n got %q", tt.want, got)
			}
		})
	}
}

func TestCommitGeneratesGroupedDefaultMessageForMultipleFiles(t *testing.T) {
	disk := newTestDisk(t)
	adapter := disk.Adapter().(*Adapter)
	seedTrackedFiles(t, disk, adapter, map[string]string{
		"docs/remove.md": "remove",
		"docs/update.md": "base",
	})
	if err := disk.Put(context.Background(), "docs/add.md", []byte("new")); err != nil {
		t.Fatalf("put added file: %v", err)
	}
	if err := disk.Put(context.Background(), "docs/update.md", []byte("changed")); err != nil {
		t.Fatalf("update file: %v", err)
	}
	if err := disk.Delete(context.Background(), "docs/remove.md"); err != nil {
		t.Fatalf("delete file: %v", err)
	}
	hash, err := adapter.Commit(context.Background(), "")
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if hash == "" {
		t.Fatalf("expected commit hash")
	}
	want := "同步 3 个文件变更\n\n新增:\n- docs/add.md\n\n更新:\n- docs/update.md\n\n删除:\n- docs/remove.md"
	if got := lastCommitMessage(t, adapter); got != want {
		t.Fatalf("unexpected commit message:\nwant:\n%s\n got:\n%s", want, got)
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

func TestSyncDoesNotCreateConflictForSequentialLocalEdit(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{
		URL:  remoteURL,
		Root: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "notes.md", []byte("")); err != nil {
		t.Fatalf("put empty file: %v", err)
	}
	if err := adapter.Sync(context.Background(), "create empty file"); err != nil {
		t.Fatalf("sync empty file: %v", err)
	}
	if err := disk.Put(context.Background(), "notes.md", []byte("local content")); err != nil {
		t.Fatalf("put updated file: %v", err)
	}
	if err := adapter.Sync(context.Background(), "update file"); err != nil {
		t.Fatalf("sync updated file: %v", err)
	}

	clone := cloneRepository(t, remoteURL)
	content, err := os.ReadFile(filepath.Join(clone, "notes.md"))
	if err != nil {
		t.Fatalf("read pushed file: %v", err)
	}
	if string(content) != "local content" {
		t.Fatalf("unexpected pushed content: %q", string(content))
	}
	conflicts, err := adapter.Conflicts(context.Background())
	if err != nil {
		t.Fatalf("list conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflict copies, got %#v", conflicts)
	}
}

func TestSyncPrunesDeletedDirectoryShells(t *testing.T) {
	remoteURL := seedBareRepository(t)
	root := t.TempDir()
	disk, err := NewDisk(context.Background(), Config{
		URL:  remoteURL,
		Root: root,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "suite/empty-child/.ll-filebrowser-directory", []byte("")); err != nil {
		t.Fatalf("put directory placeholder: %v", err)
	}
	if err := adapter.Sync(context.Background(), "create empty directory placeholder"); err != nil {
		t.Fatalf("sync placeholder: %v", err)
	}
	clone := cloneRepository(t, remoteURL)
	if _, err := os.Stat(filepath.Join(clone, "suite/empty-child/.ll-filebrowser-directory")); err != nil {
		t.Fatalf("expected placeholder on remote: %v", err)
	}

	if err := disk.DeleteDirectory(context.Background(), "suite"); err != nil {
		t.Fatalf("delete directory: %v", err)
	}
	if err := adapter.Sync(context.Background(), "delete empty directory placeholder"); err != nil {
		t.Fatalf("sync deletion: %v", err)
	}

	clone = cloneRepository(t, remoteURL)
	if _, err := os.Stat(filepath.Join(clone, "suite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected remote directory removed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "suite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected local directory shell pruned, got %v", err)
	}
}

func TestPullPrunesRemoteDeletedDirectoryShells(t *testing.T) {
	remoteURL := seedBareRepository(t)
	root := t.TempDir()
	disk, err := NewDisk(context.Background(), Config{
		URL:  remoteURL,
		Root: root,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "suite/empty-child/.ll-filebrowser-directory", []byte("")); err != nil {
		t.Fatalf("put directory placeholder: %v", err)
	}
	if err := adapter.Sync(context.Background(), "create empty directory placeholder"); err != nil {
		t.Fatalf("sync placeholder: %v", err)
	}

	removeRemotePath(t, remoteURL, "suite")
	if err := adapter.Pull(context.Background()); err != nil {
		t.Fatalf("pull deletion: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "suite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected local directory shell pruned, got %v", err)
	}
}

func TestFileOperationsWaitForSyncLock(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, disk *filesystem.Disk)
		run   func(ctx context.Context, adapter *Adapter) error
	}{
		{
			name: "open",
			run: func(ctx context.Context, adapter *Adapter) error {
				rc, err := adapter.Open(ctx, "docs/a.txt")
				if err != nil {
					return err
				}
				return rc.Close()
			},
		},
		{
			name: "exists",
			run: func(ctx context.Context, adapter *Adapter) error {
				_, err := adapter.Exists(ctx, "docs/a.txt")
				return err
			},
		},
		{
			name: "stat",
			run: func(ctx context.Context, adapter *Adapter) error {
				_, err := adapter.Stat(ctx, "docs/a.txt")
				return err
			},
		},
		{
			name: "list page",
			run: func(ctx context.Context, adapter *Adapter) error {
				_, err := adapter.ListPage(ctx, "docs", filesystem.ListOptions{})
				return err
			},
		},
		{
			name: "write",
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.Write(ctx, "docs/write.txt", strings.NewReader("write"), filesystem.DefaultWriteOptions())
			},
		},
		{
			name: "copy",
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.Copy(ctx, "docs/a.txt", "docs/copied.txt")
			},
		},
		{
			name: "move",
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.Move(ctx, "docs/a.txt", "docs/moved.txt")
			},
		},
		{
			name: "make directory",
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.MakeDirectory(ctx, "docs/new-dir", filesystem.DirectoryOptions{})
			},
		},
		{
			name: "delete",
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.Delete(ctx, "docs/a.txt")
			},
		},
		{
			name: "delete directory",
			setup: func(t *testing.T, disk *filesystem.Disk) {
				t.Helper()
				if err := disk.MakeDirectory(context.Background(), "docs/remove-dir"); err != nil {
					t.Fatalf("make remove dir: %v", err)
				}
			},
			run: func(ctx context.Context, adapter *Adapter) error {
				return adapter.DeleteDirectory(ctx, "docs/remove-dir")
			},
		},
		{
			name: "conflicts",
			run: func(ctx context.Context, adapter *Adapter) error {
				_, err := adapter.Conflicts(ctx)
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disk := newTestDisk(t)
			if err := disk.Put(context.Background(), "docs/a.txt", []byte("base")); err != nil {
				t.Fatalf("put source file: %v", err)
			}
			if tt.setup != nil {
				tt.setup(t, disk)
			}
			adapter := disk.Adapter().(*Adapter)

			adapter.mu.Lock()
			done := make(chan error, 1)
			go func() {
				done <- tt.run(context.Background(), adapter)
			}()

			select {
			case err := <-done:
				adapter.mu.Unlock()
				if err != nil {
					t.Fatalf("operation returned early with error: %v", err)
				}
				t.Fatalf("operation completed while sync lock was held")
			case <-time.After(30 * time.Millisecond):
			}

			adapter.mu.Unlock()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("operation after unlock: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatalf("operation did not complete after sync lock was released")
			}
		})
	}
}

func TestFileWriteDoesNotWaitForGitOperationLock(t *testing.T) {
	disk := newTestDisk(t)
	adapter := disk.Adapter().(*Adapter)

	adapter.gitMu.Lock()
	done := make(chan error, 1)
	go func() {
		done <- disk.Put(context.Background(), "docs/live-edit.txt", []byte("local edit while push runs"))
	}()

	select {
	case err := <-done:
		if err != nil {
			adapter.gitMu.Unlock()
			t.Fatalf("write while git operation lock is held: %v", err)
		}
	case <-time.After(time.Second):
		adapter.gitMu.Unlock()
		t.Fatalf("write waited for git operation lock")
	}
	adapter.gitMu.Unlock()

	content, err := disk.Get(context.Background(), "docs/live-edit.txt")
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(content) != "local edit while push runs" {
		t.Fatalf("unexpected written content: %q", string(content))
	}
}

func TestWriteReaderCanReadFromSameAdapter(t *testing.T) {
	disk := newTestDisk(t)
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "docs/source.txt", []byte("source from same adapter")); err != nil {
		t.Fatalf("put source: %v", err)
	}

	reader, writer := io.Pipe()
	readerDone := make(chan error, 1)
	go func() {
		rc, err := adapter.Open(context.Background(), "docs/source.txt")
		if err != nil {
			_ = writer.CloseWithError(err)
			readerDone <- err
			return
		}
		_, copyErr := io.Copy(writer, rc)
		closeErr := rc.Close()
		if copyErr == nil {
			copyErr = closeErr
		}
		if copyErr != nil {
			_ = writer.CloseWithError(copyErr)
			readerDone <- copyErr
			return
		}
		readerDone <- writer.Close()
	}()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- adapter.Write(context.Background(), "docs/from-reader.txt", reader, filesystem.DefaultWriteOptions())
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write from same adapter reader: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("write deadlocked while reader opened the same git adapter")
	}
	if err := <-readerDone; err != nil {
		t.Fatalf("reader from same adapter: %v", err)
	}
	content, err := disk.Get(context.Background(), "docs/from-reader.txt")
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(content) != "source from same adapter" {
		t.Fatalf("unexpected written content: %q", string(content))
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
		t.Fatalf("expected remote version copy, got %#v", page.Items)
	}
	if !strings.Contains(path.Base(conflictPath), remoteVersionConflictMarker) {
		t.Fatalf("expected remote version marker in conflict copy path, got %q", conflictPath)
	}
	remoteCopy, err := disk.Get(context.Background(), conflictPath)
	if err != nil {
		t.Fatalf("read remote version copy: %v", err)
	}
	if string(remoteCopy) != "remote" {
		t.Fatalf("expected remote version in remote version copy, got %q", string(remoteCopy))
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
	if got := ConflictFileName("docs/笔记.md", at); got != "docs/笔记（远端版本-20260602-2130）.md" {
		t.Fatalf("unexpected conflict name: %q", got)
	}
	if got := ConflictFileName("README", at); got != "README（远端版本-20260602-2130）" {
		t.Fatalf("unexpected extensionless conflict name: %q", got)
	}
	if !isConflictCopyName("README（远端版本-20260602-2130）") {
		t.Fatalf("expected remote version name to be detected as conflict copy")
	}
	if !isConflictCopyName("README（冲突文件-20260602-2130）") {
		t.Fatalf("expected legacy conflict name to be detected as conflict copy")
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

func seedTrackedFiles(t testing.TB, disk *filesystem.Disk, adapter *Adapter, files map[string]string) {
	t.Helper()
	for filePath, content := range files {
		if err := disk.Put(context.Background(), filePath, []byte(content)); err != nil {
			t.Fatalf("put tracked file %s: %v", filePath, err)
		}
	}
	if _, err := adapter.Commit(context.Background(), "seed tracked files"); err != nil {
		t.Fatalf("commit tracked files: %v", err)
	}
}

func lastCommitMessage(t testing.TB, adapter *Adapter) string {
	t.Helper()
	head, err := adapter.repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := adapter.repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	return commit.Message
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

func removeRemotePath(t testing.TB, remoteURL string, filePath string) {
	t.Helper()
	root := cloneRepository(t, remoteURL)
	repo, err := gogit.PlainOpen(root)
	if err != nil {
		t.Fatalf("open remote remove clone: %v", err)
	}
	target := filepath.Join(root, filepath.FromSlash(filePath))
	if err := os.RemoveAll(target); err != nil {
		t.Fatalf("remove remote path: %v", err)
	}
	commitAndPush(t, repo, "remote remove")
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
