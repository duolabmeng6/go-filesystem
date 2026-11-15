package gitdriver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem/local"
)

func TestSyncFailedPullRestoresLocalChanges(t *testing.T) {
	remote := seedBareRepository(t)
	root := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: remote, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/a.txt"), []byte("local edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/new.txt"), []byte("new local file"), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parsed.Path, parsed.Path+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Sync(context.Background(), "sync"); err == nil {
		t.Fatal("expected unavailable remote to fail")
	}
	for name, want := range map[string]string{"docs/a.txt": "local edit", "docs/new.txt": "new local file"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: got %q, error %v", name, got, err)
		}
	}
}

func TestSyncCancellationPreservesConcurrentEditAndBackup(t *testing.T) {
	remote := seedBareRepository(t)
	root := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: remote, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "docs/a.txt")
	if err := os.WriteFile(target, []byte("before sync"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	adapter.config.URL = server.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- adapter.Sync(ctx, "sync") }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("pull did not start")
	}
	if err := os.WriteFile(target, []byte("later edit"), 0o644); err != nil {
		t.Fatal(err)
	}
	cancel()
	var syncErr error
	select {
	case syncErr = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled sync did not finish")
	}
	if syncErr == nil || !errors.Is(syncErr, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", syncErr)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "later edit" {
		t.Fatalf("concurrent edit was overwritten: %q, %v", content, err)
	}
	backups, err := filepath.Glob(filepath.Join(root, ".git", "go-filesystem-sync-*", "snapshots.jsonl"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected retained backup, got %v, %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	var snapshot dirtySnapshot
	if err := json.Unmarshal(backup, &snapshot); err != nil {
		t.Fatal(err)
	}
	if string(snapshot.Content) != "before sync" {
		t.Fatalf("backup does not contain original content: %+v", snapshot)
	}
	if err := adapter.Sync(context.Background(), "retry"); err == nil || !strings.Contains(err.Error(), filepath.Dir(backups[0])) {
		t.Fatalf("expected retry to report pending backup, got %v", err)
	}
	content, err = os.ReadFile(target)
	if err != nil || string(content) != "later edit" {
		t.Fatalf("retry changed concurrent edit: %q, %v", content, err)
	}
}

func TestSyncConflictCopyDoesNotOverwriteExistingFile(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: seedBareRepository(t), Root: root})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.Local)
	conflict := filepath.Join(root, filepath.FromSlash(ConflictFileName("docs/a.txt", at)))
	if err := os.WriteFile(conflict, []byte("existing conflict"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adapter.restoreSnapshotsLocked([]dirtySnapshot{{
		Path: "docs/a.txt", Exists: true, Content: []byte("local"),
		BaseExists: true, BaseContent: []byte("old remote"),
	}}, at); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(conflict)
	if err != nil || string(data) != "existing conflict" {
		t.Fatalf("existing conflict overwritten: %q, %v", data, err)
	}
	ext := filepath.Ext(conflict)
	second := strings.TrimSuffix(conflict, ext) + "-1" + ext
	data, err = os.ReadFile(second)
	if err != nil || string(data) != "base" {
		t.Fatalf("new conflict missing: %q, %v", data, err)
	}
}

func TestSyncRejectsDirtySymlinkWithoutTouchingItsTarget(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: seedBareRepository(t), Root: root})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "docs/link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := adapter.Sync(context.Background(), "sync"); err == nil {
		t.Fatal("dirty symlink should be rejected before cleaning")
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was changed: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside content" {
		t.Fatalf("symlink target was changed: %q, %v", data, err)
	}
}

func TestSyncRejectsStagedChangesWithoutLosingEitherVersion(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: seedBareRepository(t), Root: root})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "docs/a.txt")
	if err := os.WriteFile(target, []byte("staged version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.worktree.Add("docs/a.txt"); err != nil {
		t.Fatal(err)
	}
	index, err := adapter.repo.Storer.Index()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := index.Entry("docs/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	stagedHash := entry.Hash
	if err := os.WriteFile(target, []byte("working version"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = adapter.Sync(context.Background(), "sync")
	if err == nil || !strings.Contains(err.Error(), "暂存") {
		t.Fatalf("expected staged-change error, got %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "working version" {
		t.Fatalf("working version changed: %q, %v", content, err)
	}
	index, err = adapter.repo.Storer.Index()
	if err != nil {
		t.Fatal(err)
	}
	entry, err = index.Entry("docs/a.txt")
	if err != nil || entry.Hash != stagedHash {
		t.Fatalf("staged version changed: %v", err)
	}
}

func TestSyncBackupRejectsWorktreeGitFileBeforeCleanup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /other/repo"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "local.txt")
	if err := os.WriteFile(file, []byte("local content"), 0o600); err != nil {
		t.Fatal(err)
	}
	storage, err := local.New(local.Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{files: storage}
	_, err = adapter.saveSnapshotsLocked([]dirtySnapshot{{Path: "local.txt", Exists: true, Content: []byte("local content")}})
	if err == nil || !strings.Contains(err.Error(), ".git 不是目录") {
		t.Fatalf("expected clear unsupported-worktree error, got %v", err)
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "local content" {
		t.Fatalf("local file was changed: %q, %v", content, err)
	}
}

func TestSyncCrashLeavesDiscoverableBackup(t *testing.T) {
	if os.Getenv("GOFILESYSTEM_SYNC_CRASH_HELPER") == "1" {
		root := os.Getenv("GOFILESYSTEM_SYNC_CRASH_ROOT")
		adapter, err := New(context.Background(), Config{URL: os.Getenv("GOFILESYSTEM_SYNC_CRASH_REMOTE"), Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "docs/a.txt"), []byte("crash local edit"), 0o644); err != nil {
			t.Fatal(err)
		}
		adapter.config.URL = os.Getenv("GOFILESYSTEM_SYNC_CRASH_BLOCKING_REMOTE")
		_ = adapter.Sync(context.Background(), "sync")
		t.Fatal("blocking pull unexpectedly returned")
	}
	remote := seedBareRepository(t)
	root := t.TempDir()
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSyncCrashLeavesDiscoverableBackup$")
	cmd.Env = append(os.Environ(),
		"GOFILESYSTEM_SYNC_CRASH_HELPER=1",
		"GOFILESYSTEM_SYNC_CRASH_ROOT="+root,
		"GOFILESYSTEM_SYNC_CRASH_REMOTE="+remote,
		"GOFILESYSTEM_SYNC_CRASH_BLOCKING_REMOTE="+server.URL,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("child sync did not start pull")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	adapter, err := New(context.Background(), Config{URL: remote, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	backups, err := filepath.Glob(filepath.Join(root, ".git", "go-filesystem-sync-*", "snapshots.jsonl"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("expected crash backup, got %v, %v", backups, err)
	}
	backup, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	var snapshot dirtySnapshot
	if err := json.Unmarshal(backup, &snapshot); err != nil || string(snapshot.Content) != "crash local edit" {
		t.Fatalf("invalid crash backup: %+v, %v", snapshot, err)
	}
	if err := adapter.Sync(context.Background(), "retry"); err == nil || !strings.Contains(err.Error(), filepath.Dir(backups[0])) {
		t.Fatalf("retry did not report crash backup: %v", err)
	}
}
