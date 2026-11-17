package gitdriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func preparedFixture(t *testing.T) (string, string, *Adapter, *filesystem.Disk) {
	t.Helper()
	remote := seedBareRepository(t)
	root := t.TempDir()
	disk, err := NewDisk(context.Background(), Config{URL: remote, Root: root, Branch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	return remote, root, disk.Adapter().(*Adapter), disk
}

func TestPreparedSyncPersistsAndPushesFixedCommit(t *testing.T) {
	ctx := context.Background()
	remote, root, adapter, disk := preparedFixture(t)
	bare := openBareForBindingTest(t, remote)
	branch := plumbing.NewBranchReferenceName("master")
	before, err := bare.Reference(branch, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(ctx, "docs/prepared.txt", []byte("prepared")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(ctx, "prepare")
	if err != nil {
		t.Fatal(err)
	}
	current, err := bare.Reference(branch, true)
	if err != nil || current.Hash() != before.Hash() {
		t.Fatalf("prepare changed remote: %v", err)
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), remote) {
		t.Fatal("intent contains remote URL")
	}
	var restored PreparedSync
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	// A new adapter models reopening the same worktree after a process restart.
	reopened, err := New(ctx, Config{URL: remote, Root: root, Branch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Write(ctx, "docs/uncommitted.txt", strings.NewReader("later"), filesystem.WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DispatchSync(ctx, restored); err != nil {
		t.Fatal(err)
	}
	current, err = bare.Reference(branch, true)
	if err != nil || current.Hash().String() != intent.Commit {
		t.Fatalf("wrong remote commit: %v", err)
	}
	clone := cloneRepository(t, remote)
	if data, err := os.ReadFile(filepath.Join(clone, "docs/prepared.txt")); err != nil || string(data) != "prepared" {
		t.Fatalf("prepared file: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(clone, "docs/uncommitted.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncommitted file reached remote: %v", err)
	}
}

func TestPreparedSyncDispatchInSeparateProcess(t *testing.T) {
	remote, root, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/process", []byte("prepared")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	intentPath := filepath.Join(t.TempDir(), "intent.json")
	if err := os.WriteFile(intentPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPreparedSyncResumeInChild$", "-test.v")
	cmd.Env = append(os.Environ(), "GOFS_PREPARED_CHILD=1", "GOFS_PREPARED_ROOT="+root,
		"GOFS_PREPARED_REMOTE="+remote, "GOFS_PREPARED_INTENT="+intentPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("resume child failed: %v\n%s", err, output)
	}
	ref, err := openBareForBindingTest(t, remote).Reference(plumbing.NewBranchReferenceName("master"), true)
	if err != nil || ref.Hash().String() != intent.Commit {
		t.Fatalf("child did not push prepared commit: %v", err)
	}
}

func TestPreparedSyncResumeInChild(t *testing.T) {
	if os.Getenv("GOFS_PREPARED_CHILD") != "1" {
		t.Skip("only used by the isolated child process")
	}
	encoded, err := os.ReadFile(os.Getenv("GOFS_PREPARED_INTENT"))
	if err != nil {
		t.Fatal(err)
	}
	var intent PreparedSync
	if err := json.Unmarshal(encoded, &intent); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(context.Background(), Config{
		URL: os.Getenv("GOFS_PREPARED_REMOTE"), Root: os.Getenv("GOFS_PREPARED_ROOT"), Branch: "master",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.DispatchSync(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedSyncRejectsInvalidOrStaleIntent(t *testing.T) {
	cases := map[string]func(*PreparedSync){
		"version":          func(i *PreparedSync) { i.Version++ },
		"root":             func(i *PreparedSync) { i.Root += "-different" },
		"worktree":         func(i *PreparedSync) { i.WorktreeID += "x" },
		"git-dir":          func(i *PreparedSync) { i.GitDirID += "x" },
		"remote":           func(i *PreparedSync) { i.RemoteDigest = strings.Repeat("0", 64) },
		"branch-injection": func(i *PreparedSync) { i.Branch = "refs/heads/master:refs/heads/other" },
		"invalid-hash":     func(i *PreparedSync) { i.Commit = strings.Repeat("z", 40) },
		"zero-hash":        func(i *PreparedSync) { i.Commit = strings.Repeat("0", 40) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			remote, _, adapter, disk := preparedFixture(t)
			if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
				t.Fatal(err)
			}
			intent, err := adapter.PrepareSync(context.Background(), "prepare")
			if err != nil {
				t.Fatal(err)
			}
			before, err := openBareForBindingTest(t, remote).Reference(plumbing.NewBranchReferenceName("master"), true)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&intent)
			if err := adapter.DispatchSync(context.Background(), intent); !errors.Is(err, filesystem.ErrInvalidPath) {
				t.Fatalf("invalid intent accepted: %v", err)
			}
			after, err := openBareForBindingTest(t, remote).Reference(plumbing.NewBranchReferenceName("master"), true)
			if err != nil || after.Hash() != before.Hash() {
				t.Fatalf("remote changed: %v", err)
			}
		})
	}
}

func TestPreparedSyncRejectsNewCommit(t *testing.T) {
	remote, _, adapter, disk := preparedFixture(t)
	ctx := context.Background()
	if err := disk.Put(ctx, "docs/first", []byte("first")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(ctx, "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Put(ctx, "docs/second", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Commit(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	if err := adapter.DispatchSync(ctx, intent); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("stale commit accepted: %v", err)
	}
	remoteRef, err := openBareForBindingTest(t, remote).Reference(plumbing.NewBranchReferenceName("master"), true)
	if err != nil || remoteRef.Hash().String() == intent.Commit {
		t.Fatalf("stale commit was pushed: %v", err)
	}
}

func TestPreparedSyncRejectsReplacedRepositoryAfterReopen(t *testing.T) {
	remote, root, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	// Clone the prepared commit and keep the same remote and branch. Only the
	// physical worktree identity changes, so a hash check alone cannot pass.
	replacement := filepath.Join(t.TempDir(), "replacement")
	localURL := (&url.URL{Scheme: "file", Path: root}).String()
	clone, err := gogit.PlainClone(replacement, false, &gogit.CloneOptions{URL: localURL})
	if err != nil {
		t.Fatal(err)
	}
	setRepositoryOriginForSyncTest(t, clone, remote)
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, root); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(context.Background(), Config{URL: remote, Root: root, Branch: "master"})
	if err != nil {
		t.Fatal(err)
	}
	head, err := reopened.repo.Head()
	if err != nil || head.Hash().String() != intent.Commit {
		t.Fatalf("replacement does not contain prepared commit: %v", err)
	}
	if err := reopened.DispatchSync(context.Background(), intent); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("replacement accepted: %v", err)
	}
}

func TestPreparedSyncRejectsGitPointerBeforeMutation(t *testing.T) {
	for _, mode := range []string{"gitfile", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			remote, root, _, disk := preparedFixture(t)
			if err := disk.Put(ctx, "docs/change", []byte("local")); err != nil {
				t.Fatal(err)
			}
			gitDir := filepath.Join(root, ".git")
			actual := filepath.Join(t.TempDir(), "actual-git")
			if err := os.Rename(gitDir, actual); err != nil {
				t.Fatal(err)
			}
			if mode == "gitfile" {
				if err := os.WriteFile(gitDir, []byte("gitdir: "+actual+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink(actual, gitDir); err != nil {
				t.Skipf("cannot create directory symlink: %v", err)
			}
			adapter, err := New(ctx, Config{URL: remote, Root: root, Branch: "master"})
			if err != nil {
				if mode == "symlink" {
					data, readErr := os.ReadFile(filepath.Join(root, "docs", "change"))
					if readErr != nil || string(data) != "local" {
						t.Fatalf("local change was lost: %q, %v", data, readErr)
					}
					return
				}
				t.Fatal(err)
			}
			before, err := adapter.repo.Head()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.PrepareSync(ctx, "must not commit"); !errors.Is(err, filesystem.ErrInvalidPath) {
				t.Fatalf("non-directory .git accepted: %v", err)
			}
			after, err := adapter.repo.Head()
			if err != nil || before.Hash() != after.Hash() {
				t.Fatalf("prepare changed HEAD: %v", err)
			}
			status, err := adapter.Status(ctx)
			if err != nil || !status.Dirty {
				t.Fatalf("local change was lost: %#v, %v", status, err)
			}
		})
	}
}

func TestPreparedSyncRejectsChangedDefaultBranch(t *testing.T) {
	remote := seedBareRepository(t)
	adapter, err := New(context.Background(), Config{URL: remote, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	bare := openBareForBindingTest(t, remote)
	master, err := bare.Reference(plumbing.NewBranchReferenceName("master"), true)
	if err != nil {
		t.Fatal(err)
	}
	other := plumbing.NewBranchReferenceName("other")
	if err := bare.Storer.SetReference(plumbing.NewHashReference(other, master.Hash())); err != nil {
		t.Fatal(err)
	}
	if err := bare.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, other)); err != nil {
		t.Fatal(err)
	}
	if err := adapter.DispatchSync(context.Background(), intent); !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("changed remote default accepted: %v", err)
	}
}

func TestPreparedSyncReportsExistingBackup(t *testing.T) {
	_, root, adapter, _ := preparedFixture(t)
	backup := filepath.Join(root, ".git", "go-filesystem-sync-pending")
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.PrepareSync(context.Background(), "prepare")
	if err == nil || !strings.Contains(err.Error(), backup) {
		t.Fatalf("backup location missing from error: %v", err)
	}
}

func TestPreparedSyncLockWaitHonorsCancellation(t *testing.T) {
	_, _, adapter, _ := preparedFixture(t)
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	adapter.gitMu.Lock()
	defer adapter.gitMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := adapter.PrepareSync(ctx, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait returned %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancelled lock wait took too long")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel2()
	if err := adapter.DispatchSync(ctx2, intent); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dispatch lock wait returned %v", err)
	}
}

func TestPreparedSyncPushErrorDoesNotExposeRemote(t *testing.T) {
	_, _, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "transport details FAKESECRET", http.StatusInternalServerError)
	}))
	defer server.Close()
	remote := strings.Replace(server.URL, "http://", "http://alice:FAKEPASSWORD@", 1) + "/repo?signature=FAKESECRET"
	setRepositoryOriginForSyncTest(t, adapter.repo, remote)
	adapter.config.URL = remote
	intent.RemoteDigest = preparedRemoteDigest(adapter.config)
	err = adapter.DispatchSync(context.Background(), intent)
	if err == nil || strings.Contains(err.Error(), "FAKESECRET") || strings.Contains(err.Error(), "FAKEPASSWORD") || strings.Contains(err.Error(), remote) {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}

func TestPreparedSyncDiscoveryErrorDoesNotExposeRemote(t *testing.T) {
	_, _, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "transport details FAKESECRET", http.StatusInternalServerError)
	}))
	defer server.Close()
	remote := strings.Replace(server.URL, "http://", "http://alice:FAKEPASSWORD@", 1) + "/repo?signature=FAKESECRET"
	setRepositoryOriginForSyncTest(t, adapter.repo, remote)
	adapter.config.URL = remote
	adapter.config.Branch = ""
	intent.RemoteDigest = preparedRemoteDigest(adapter.config)
	err = adapter.DispatchSync(context.Background(), intent)
	if err == nil || strings.Contains(err.Error(), "FAKESECRET") || strings.Contains(err.Error(), "FAKEPASSWORD") || strings.Contains(err.Error(), remote) {
		t.Fatalf("unsafe or missing discovery error: %v", err)
	}
}

func TestPreparedSyncPrepareErrorDoesNotExposeRemote(t *testing.T) {
	_, _, adapter, _ := preparedFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "transport details FAKESECRET", http.StatusInternalServerError)
	}))
	defer server.Close()
	remote := strings.Replace(server.URL, "http://", "http://alice:FAKEPASSWORD@", 1) + "/repo?signature=FAKESECRET"
	setRepositoryOriginForSyncTest(t, adapter.repo, remote)
	adapter.config.URL = remote
	_, err := adapter.PrepareSync(context.Background(), "prepare")
	if err == nil || strings.Contains(err.Error(), "FAKESECRET") || strings.Contains(err.Error(), "FAKEPASSWORD") || strings.Contains(err.Error(), remote) {
		t.Fatalf("unsafe or missing error: %v", err)
	}
}

func TestPreparedSyncReportsBackupWithoutLeakingPullError(t *testing.T) {
	_, root, adapter, _ := preparedFixture(t)
	tracked := filepath.Join(root, "docs", "a.txt")
	if err := os.WriteFile(tracked, []byte("local change"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Simulate a local conflict arising while the remote pull is in flight.
		err := os.Remove(tracked)
		if err == nil {
			err = os.Mkdir(tracked, 0o700)
		}
		changed <- err
		http.Error(w, "transport details FAKESECRET", http.StatusInternalServerError)
	}))
	defer server.Close()
	remote := strings.Replace(server.URL, "http://", "http://alice:FAKEPASSWORD@", 1) + "/repo?signature=FAKESECRET"
	setRepositoryOriginForSyncTest(t, adapter.repo, remote)
	adapter.config.URL = remote
	_, err := adapter.PrepareSync(context.Background(), "prepare")
	select {
	case changeErr := <-changed:
		if changeErr != nil {
			t.Fatal(changeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("pull did not reach the test server")
	}
	backups, globErr := filepath.Glob(filepath.Join(root, ".git", "go-filesystem-sync-*", "snapshots.jsonl"))
	if globErr != nil || len(backups) != 1 {
		t.Fatalf("expected retained backup: %v, %v", backups, globErr)
	}
	if err == nil || !strings.Contains(err.Error(), backups[0]) || strings.Contains(err.Error(), "FAKESECRET") || strings.Contains(err.Error(), "FAKEPASSWORD") {
		t.Fatalf("unsafe or incomplete recovery error: %v", err)
	}
}

func TestPreparedSyncAllowsLocalEditDuringPush(t *testing.T) {
	_, _, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
		http.Error(w, "stopped", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	defer unblock()
	setRepositoryOriginForSyncTest(t, adapter.repo, server.URL)
	adapter.config.URL = server.URL
	intent.RemoteDigest = preparedRemoteDigest(adapter.config)
	done := make(chan error, 1)
	go func() { done <- adapter.DispatchSync(context.Background(), intent) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("push did not reach remote")
	}
	writeDone := make(chan error, 1)
	go func() { writeDone <- disk.Put(context.Background(), "docs/later", []byte("later")) }()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("local edit blocked behind network push")
	}
	unblock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected test server push failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not finish after server response")
	}
}

func TestPreparedSyncRechecksHeadAfterRemoteDiscovery(t *testing.T) {
	_, _, adapter, disk := preparedFixture(t)
	if err := disk.Put(context.Background(), "docs/change", []byte("content")); err != nil {
		t.Fatal(err)
	}
	intent, err := adapter.PrepareSync(context.Background(), "prepare")
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	advertised := pkt("# service=git-upload-pack\n") + "0000" +
		pkt(intent.Commit+" HEAD\x00symref=HEAD:refs/heads/master\n") +
		pkt(intent.Commit+" refs/heads/master\n") + "0000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("service") != "git-upload-pack" {
			http.Error(w, "unexpected push", http.StatusBadRequest)
			return
		}
		select {
		case reached <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte(advertised))
	}))
	defer server.Close()
	defer unblock()
	setRepositoryOriginForSyncTest(t, adapter.repo, server.URL)
	adapter.config.URL = server.URL
	adapter.config.Branch = ""
	intent.RemoteDigest = preparedRemoteDigest(adapter.config)
	done := make(chan error, 1)
	go func() { done <- adapter.DispatchSync(context.Background(), intent) }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("remote discovery did not start")
	}
	other := plumbing.NewBranchReferenceName("other")
	if err := adapter.repo.Storer.SetReference(plumbing.NewHashReference(other, plumbing.NewHash(intent.Commit))); err != nil {
		t.Fatal(err)
	}
	if err := adapter.repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, other)); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Fatalf("changed HEAD was not rejected: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch did not return")
	}
}

func TestPreparedRemoteDigestOmitsSecrets(t *testing.T) {
	first := Config{URL: "https://user:secret@example.com/repo?signature=token", AuthMode: AuthModePassword, Username: "alice"}
	second := first
	second.URL = "https://user:rotated@example.com/repo?signature=token"
	if preparedRemoteDigest(first) != preparedRemoteDigest(second) {
		t.Fatal("password rotation unexpectedly changed remote identity")
	}
	intent := PreparedSync{RemoteDigest: preparedRemoteDigest(first)}
	data, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret", "signature", "token", "example.com"} {
		if strings.Contains(string(data), secret) {
			t.Fatal(fmt.Sprintf("remote detail %q leaked", secret))
		}
	}
}
