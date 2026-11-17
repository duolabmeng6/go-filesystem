package gitdriver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
)

func openBareForBindingTest(t *testing.T, raw string) *gogit.Repository {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := gogit.PlainOpen(u.Path)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestGitCacheRejectsDifferentRepository(t *testing.T) {
	oldURL := seedBareRepository(t)
	newURL := emptyBareRepository(t)
	cache := t.TempDir()
	if _, err := New(context.Background(), Config{URL: oldURL, Root: cache}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(cache, "docs", "local.txt")
	if err := os.WriteFile(local, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{URL: newURL, Root: cache}); !errors.Is(err, filesystem.ErrInvalidPath) || !errors.Is(err, ErrCacheMismatch) {
		t.Fatalf("reuse with different repository: %v", err)
	}
	if content, err := os.ReadFile(local); err != nil || string(content) != "preserve" {
		t.Fatalf("existing worktree changed: %q, %v", content, err)
	}
	if _, err := openBareForBindingTest(t, newURL).Reference(plumbing.NewBranchReferenceName("master"), true); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("other remote unexpectedly changed: %v", err)
	}
}

func TestGitCacheRejectsDifferentBranchAndDetachedHead(t *testing.T) {
	remoteURL := seedBareRepository(t)
	cache := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: remoteURL, Root: cache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{URL: remoteURL, Root: cache, Branch: "other"}); !errors.Is(err, filesystem.ErrInvalidPath) || !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("reuse with different branch: %v", err)
	}
	head, err := adapter.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.repo.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, head.Hash())); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{URL: remoteURL, Root: cache}); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("reopen detached HEAD: %v", err)
	}
	if err := adapter.Sync(context.Background(), "must not commit"); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("sync detached HEAD: %v", err)
	}
}

func TestGitRemoteChangedAfterOpenRejectsRemoteOperations(t *testing.T) {
	oldURL := seedBareRepository(t)
	newURL := emptyBareRepository(t)
	adapter, err := New(context.Background(), Config{URL: oldURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	config, err := adapter.repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	config.Remotes[defaultRemoteName].URLs = []string{newURL}
	if err := adapter.repo.SetConfig(config); err != nil {
		t.Fatal(err)
	}
	for name, op := range map[string]func() error{
		"pull": func() error { return adapter.Pull(context.Background()) },
		"push": func() error { return adapter.Push(context.Background()) },
		"sync": func() error { return adapter.Sync(context.Background(), "must not commit") },
	} {
		if err := op(); !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Errorf("%s with changed origin: %v", name, err)
		}
	}
	if _, err := openBareForBindingTest(t, newURL).Reference(plumbing.NewBranchReferenceName("master"), true); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("other remote unexpectedly changed: %v", err)
	}
}

func TestGitPushOnlyConfiguredOrRemoteDefaultBranch(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit=%v", explicit), func(t *testing.T) {
			remoteURL := seedBareRepository(t)
			config := Config{URL: remoteURL, Root: t.TempDir()}
			if explicit {
				config.Branch = "master"
			}
			adapter, err := New(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			head, err := adapter.repo.Head()
			if err != nil {
				t.Fatal(err)
			}
			extra := plumbing.NewBranchReferenceName("extra-branch")
			if err := adapter.repo.Storer.SetReference(plumbing.NewHashReference(extra, head.Hash())); err != nil {
				t.Fatal(err)
			}
			if err := adapter.Push(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := openBareForBindingTest(t, remoteURL).Reference(extra, true); !errors.Is(err, plumbing.ErrReferenceNotFound) {
				t.Fatalf("extra branch pushed: %v", err)
			}
		})
	}
}

func TestGitSyncDoesNotPushExtraLocalBranch(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	adapter := disk.Adapter().(*Adapter)
	head, err := adapter.repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	extra := plumbing.NewBranchReferenceName("extra-branch")
	if err := adapter.repo.Storer.SetReference(plumbing.NewHashReference(extra, head.Hash())); err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(context.Background(), "docs/new.txt", []byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Sync(context.Background(), "sync only default"); err != nil {
		t.Fatal(err)
	}
	bare := openBareForBindingTest(t, remoteURL)
	if _, err := bare.Reference(extra, true); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("extra branch pushed during sync: %v", err)
	}
	clone := cloneRepository(t, remoteURL)
	if data, err := os.ReadFile(filepath.Join(clone, "docs", "new.txt")); err != nil || string(data) != "content" {
		t.Fatalf("default branch missing update: %q, %v", data, err)
	}
}

func TestGitPushDoesNotForceRemoteBranch(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	adapter := disk.Adapter().(*Adapter)
	if err := disk.Put(context.Background(), "docs/local.txt", []byte("local")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Commit(context.Background(), "local commit"); err != nil {
		t.Fatal(err)
	}
	updateRemoteFile(t, remoteURL, "docs/remote.txt", "remote")
	bare := openBareForBindingTest(t, remoteURL)
	branch := plumbing.NewBranchReferenceName("master")
	before, err := bare.Reference(branch, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Push(context.Background()); err == nil {
		t.Fatal("non-fast-forward push unexpectedly succeeded")
	}
	after, err := bare.Reference(branch, true)
	if err != nil || after.Hash() != before.Hash() {
		t.Fatalf("remote branch changed after rejected push: %v", err)
	}
}

func TestGitPushRejectsChangedRemoteDefault(t *testing.T) {
	remoteURL := seedBareRepository(t)
	adapter, err := New(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bare := openBareForBindingTest(t, remoteURL)
	head, err := bare.Reference(plumbing.NewBranchReferenceName("master"), true)
	if err != nil {
		t.Fatal(err)
	}
	other := plumbing.NewBranchReferenceName("other")
	if err := bare.Storer.SetReference(plumbing.NewHashReference(other, head.Hash())); err != nil {
		t.Fatal(err)
	}
	if err := bare.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, other)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Push(context.Background()); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("push with changed default branch: %v", err)
	}
}

func TestGitEmptyRemoteWithoutConfiguredBranchNeedsExplicitBranch(t *testing.T) {
	remoteURL := emptyBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(context.Background(), "README.md", []byte("preserve")); err != nil {
		t.Fatal(err)
	}
	if err := disk.Adapter().(*Adapter).Sync(context.Background(), "first"); !errors.Is(err, filesystem.ErrInvalidPath) || !errors.Is(err, ErrDefaultBranchUnclear) {
		t.Fatalf("sync without remote default: %v", err)
	}
	if got, err := disk.Get(context.Background(), "README.md"); err != nil || string(got) != "preserve" {
		t.Fatalf("worktree changed: %q, %v", got, err)
	}
}

func TestGitCacheStillReadableWhenRemoteOffline(t *testing.T) {
	remoteURL := seedBareRepository(t)
	cache := t.TempDir()
	if _, err := New(context.Background(), Config{URL: remoteURL, Root: cache}); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(remoteURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(u.Path, u.Path+"-offline"); err != nil {
		t.Fatal(err)
	}
	disk, err := NewDisk(context.Background(), Config{URL: remoteURL, Root: cache})
	if err != nil {
		t.Fatalf("offline cache open: %v", err)
	}
	if got, err := disk.Get(context.Background(), "docs/a.txt"); err != nil || string(got) != "base" {
		t.Fatalf("offline cache read: %q, %v", got, err)
	}
	if err := disk.Adapter().(*Adapter).Pull(context.Background()); err == nil {
		t.Fatal("pull should report unavailable remote")
	}
}

func TestGitCacheReplacementAfterOpenIsRejected(t *testing.T) {
	remoteURL := seedBareRepository(t)
	parent := t.TempDir()
	cache := filepath.Join(parent, "cache")
	adapter, err := New(context.Background(), Config{URL: remoteURL, Root: cache})
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(parent, "replacement")
	if _, err := gogit.PlainClone(replacement, false, &gogit.CloneOptions{URL: remoteURL}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cache, filepath.Join(parent, "previous")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, cache); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Push(context.Background()); !errors.Is(err, ErrCacheMismatch) {
		t.Fatalf("replaced cache must not be pushed: %v", err)
	}
}

func TestGitPushUsesFrozenCommit(t *testing.T) {
	remoteURL := seedBareRepository(t)
	disk, err := NewDisk(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	adapter := disk.Adapter().(*Adapter)
	branch := plumbing.NewBranchReferenceName("master")
	adapter.mu.Lock()
	frozen, err := adapter.branchCommitLocked(branch)
	adapter.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(context.Background(), "docs/new.txt", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Commit(context.Background(), "new local commit"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.pushCommit(context.Background(), branch, frozen); err != nil {
		t.Fatal(err)
	}
	remote, err := openBareForBindingTest(t, remoteURL).Reference(branch, true)
	if err != nil || remote.Hash() != frozen {
		t.Fatalf("push changed from frozen commit: %v", err)
	}
}

func TestRemoteDefaultDiscoveryErrorDoesNotExposeURL(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	err = safeRemoteDiscoveryError(fmt.Errorf("Get http://%s/repo?signature=FAKESECRET: %w", address, context.Canceled))
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "FAKESECRET") {
		t.Fatalf("unsafe or unclassifiable error: %v", err)
	}
	remoteURL := seedBareRepository(t)
	adapter, err := New(context.Background(), Config{URL: remoteURL, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	setBlockingRemoteForSyncTest(t, adapter, "http://"+address+"/repo?signature=FAKESECRET")
	err = adapter.Push(context.Background())
	var networkErr net.Error
	if err == nil || strings.Contains(err.Error(), "FAKESECRET") || !errors.As(err, &networkErr) {
		t.Fatalf("unsafe or unclassifiable discovery error: %v", err)
	}
}

func TestGitSyncReportsPendingBackupWhenRemoteOffline(t *testing.T) {
	remoteURL := seedBareRepository(t)
	cache := t.TempDir()
	adapter, err := New(context.Background(), Config{URL: remoteURL, Root: cache})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(cache, ".git", "go-filesystem-sync-pending")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(remoteURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(u.Path, u.Path+"-offline"); err != nil {
		t.Fatal(err)
	}
	err = adapter.Sync(context.Background(), "sync")
	if err == nil || !strings.Contains(err.Error(), backup) {
		t.Fatalf("pending backup should be reported even while offline: %v", err)
	}
}

func TestGitRemoteIdentityPreservesTargetAndHidesPassword(t *testing.T) {
	const base = "https://alice:old@example.test:8443/team/repo.git?token=abc"
	if gitRemoteIdentity(base) != gitRemoteIdentity("https://alice:new@example.test:8443/team/repo.git?token=abc") {
		t.Fatal("password rotation changed repository identity")
	}
	for _, other := range []string{
		"http://alice:old@example.test:8443/team/repo.git?token=abc",
		"https://alice:old@example.test:8444/team/repo.git?token=abc",
		"https://bob:old@example.test:8443/team/repo.git?token=abc",
		"https://alice:old@example.test:8443/team/other.git?token=abc",
		"https://alice:old@example.test:8443/team/repo.git?token=other",
	} {
		if gitRemoteIdentity(base) == gitRemoteIdentity(other) {
			t.Fatal("different target was accepted")
		}
	}
	if strings.Contains(gitRemoteIdentity(base), "old") {
		t.Fatal("identity retained password")
	}
}

func TestGitCacheAllowsURLPasswordRotationWithoutNetwork(t *testing.T) {
	cache := t.TempDir()
	repo, err := gogit.PlainInit(cache, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: defaultRemoteName,
		URLs: []string{"https://alice:OLDSECRET@example.test:8443/team/repo.git?route=one"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Config{
		URL: "https://alice:NEWSECRET@example.test:8443/team/repo.git?route=one", Root: cache,
	}); err != nil {
		t.Fatalf("password rotation should keep cache usable offline: %v", err)
	}
	_, err = New(context.Background(), Config{
		URL: "https://alice:NEWSECRET@example.test:8443/team/repo.git?route=other", Root: cache,
	})
	if !errors.Is(err, ErrCacheMismatch) || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "route=") {
		t.Fatalf("query mismatch must fail without exposing URL: %v", err)
	}
}

func TestAdvertisedDefaultBranchRequiresActualSymref(t *testing.T) {
	refs := packp.NewAdvRefs()
	hash := plumbing.NewHash(strings.Repeat("1", 40))
	refs.Head = &hash
	refs.References["refs/heads/master"] = hash
	if _, err := advertisedDefaultBranch(refs); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("missing symref: %v", err)
	}
	if err := refs.Capabilities.Add(capability.SymRef, "HEAD:refs/heads/master"); err != nil {
		t.Fatal(err)
	}
	if branch, err := advertisedDefaultBranch(refs); err != nil || branch != plumbing.NewBranchReferenceName("master") {
		t.Fatalf("valid symref: %s, %v", branch, err)
	}
	if err := refs.Capabilities.Add(capability.SymRef, "HEAD:refs/heads/other"); err != nil {
		t.Fatal(err)
	}
	if _, err := advertisedDefaultBranch(refs); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("ambiguous symref: %v", err)
	}
}
