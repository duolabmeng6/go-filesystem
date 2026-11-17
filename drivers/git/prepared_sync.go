package gitdriver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// PreparedSync identifies a fixed local commit and its intended destination.
// It can be persisted by a coordinating caller between preparation and push.
// It contains no raw remote URL or credentials. The caller must protect the
// intent against corruption and replay and coordinate all worktree mutations.
// File identities may change across filesystem migrations or be reused; they
// are not a defense against non-cooperating processes or malicious changes.
type PreparedSync struct {
	Version      int    `json:"version"`
	Root         string `json:"root"`
	WorktreeID   string `json:"worktree_id"`
	GitDirID     string `json:"git_dir_id"`
	RemoteDigest string `json:"remote_digest"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
}

// PrepareSync pulls and commits using the same local recovery procedure as
// Sync, without pushing. A failure may leave a local commit or recovery backup;
// the caller must retain an appropriate pending state until it is inspected.
// The caller must serialize the complete transaction with all other adapters
// and processes that can modify this worktree or push to the same remote.
// This API currently requires a real .git directory, not a gitdir pointer
// file or symlink; such worktrees may continue to use Sync.
func (a *Adapter) PrepareSync(ctx context.Context, message string) (PreparedSync, error) {
	if err := ctx.Err(); err != nil {
		return PreparedSync{}, err
	}
	if a.config.ReadOnly {
		return PreparedSync{}, filesystem.ErrReadOnly
	}
	if err := a.lockGitContext(ctx); err != nil {
		return PreparedSync{}, err
	}
	defer a.gitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return PreparedSync{}, err
	}
	a.mu.Lock()
	err := a.validateRepositoryBinding()
	var initialRoot, initialGit string
	if err == nil {
		initialRoot, initialGit, err = a.preparedFileIdentities()
	}
	a.mu.Unlock()
	if err != nil {
		return PreparedSync{}, err
	}
	branch, hash, err := a.prepareSyncLocked(ctx, strings.TrimSpace(message))
	if err != nil {
		var backup *syncBackupRecoveryError
		if errors.As(err, &backup) {
			return PreparedSync{}, &preparedSyncBackupError{path: backup.path, cause: err}
		}
		var pull *syncPullFailure
		if errors.As(err, &pull) {
			// Transport errors can include the remote URL and credentials.
			return PreparedSync{}, &preparedSyncError{cause: err}
		}
		return PreparedSync{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PreparedSync{}, err
	}
	if err := a.validateRepositoryBinding(); err != nil {
		return PreparedSync{}, err
	}
	current, err := a.branchCommitLocked(branch)
	if err != nil {
		return PreparedSync{}, err
	}
	if current != hash {
		return PreparedSync{}, stalePreparedSync("Git 提交在准备期间发生变化")
	}
	worktreeID, gitDirID, err := a.preparedFileIdentities()
	if err != nil {
		return PreparedSync{}, err
	}
	if worktreeID != initialRoot || gitDirID != initialGit {
		return PreparedSync{}, stalePreparedSync("Git 工作区或仓库在准备期间已更换")
	}
	if err := a.validateRepositoryBinding(); err != nil {
		return PreparedSync{}, err
	}
	return PreparedSync{
		Version: 1, Root: a.files.Root(), WorktreeID: worktreeID, GitDirID: gitDirID,
		RemoteDigest: preparedRemoteDigest(a.config), Branch: branch.String(), Commit: hash.String(),
	}, nil
}

// DispatchSync pushes exactly the prepared commit after rechecking its local
// worktree, repository, remote and branch. It rejects a changed branch HEAD;
// ordinary uncommitted file edits remain possible during the network push.
// This method does not by itself provide cross-process locking, durable
// transaction tracking, protection against replay, or remote result recovery.
func (a *Adapter) DispatchSync(ctx context.Context, intent PreparedSync) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.config.ReadOnly {
		return filesystem.ErrReadOnly
	}
	branch := plumbing.ReferenceName(intent.Branch)
	if intent.Version != 1 || intent.Root == "" || intent.WorktreeID == "" || intent.GitDirID == "" ||
		len(intent.RemoteDigest) != sha256.Size*2 || !branch.IsBranch() || branch.Validate() != nil ||
		len(intent.Commit) != 40 || !plumbing.IsHash(intent.Commit) || plumbing.NewHash(intent.Commit).IsZero() {
		return stalePreparedSync("Git 推送意图无效")
	}
	if err := a.lockGitContext(ctx); err != nil {
		return err
	}
	defer a.gitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	err := a.validatePreparedSyncLocked(intent, branch)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.dispatchPreparedCommit(ctx, intent, branch)
}

func (a *Adapter) validatePreparedSyncLocked(intent PreparedSync, branch plumbing.ReferenceName) error {
	if err := a.validateRepositoryBinding(); err != nil {
		return err
	}
	if intent.Root != a.files.Root() || intent.RemoteDigest != preparedRemoteDigest(a.config) {
		return stalePreparedSync("Git 工作区或远端与推送意图不一致")
	}
	worktreeID, gitDirID, err := a.preparedFileIdentities()
	if err != nil {
		return err
	}
	if intent.WorktreeID != worktreeID || intent.GitDirID != gitDirID {
		return stalePreparedSync("Git 工作区或仓库已更换")
	}
	current, err := a.branchCommitLocked(branch)
	if err != nil {
		return err
	}
	if current.String() != intent.Commit {
		return stalePreparedSync("Git 当前提交已变化，请重新核对")
	}
	return a.validateRepositoryBinding()
}

func (a *Adapter) preparedFileIdentities() (string, string, error) {
	rootID, err := gitFilesystemIdentity(a.files.Root())
	if err != nil {
		return "", "", fmt.Errorf("%w: 无法确认 Git 工作区身份: %w", filesystem.ErrInvalidPath, err)
	}
	gitDir := filepath.Join(a.files.Root(), ".git")
	info, err := os.Lstat(gitDir)
	if err != nil {
		return "", "", fmt.Errorf("%w: 无法确认 Git 仓库身份: %w", filesystem.ErrInvalidPath, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", stalePreparedSync("分阶段同步要求 .git 为实际目录")
	}
	gitID, err := gitFilesystemIdentity(gitDir)
	if err != nil {
		return "", "", fmt.Errorf("%w: 无法确认 Git 仓库身份: %w", filesystem.ErrInvalidPath, err)
	}
	return rootID, gitID, nil
}

func preparedRemoteDigest(config Config) string {
	sum := sha256.Sum256([]byte(gitRemoteIdentity(config.URL) + "\x00" + config.AuthMode + "\x00" + config.Username))
	return hex.EncodeToString(sum[:])
}

func stalePreparedSync(message string) error {
	return fmt.Errorf("%w: %s", filesystem.ErrInvalidPath, message)
}

func (a *Adapter) lockGitContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.gitMu.TryLock() {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (a *Adapter) dispatchPreparedCommit(ctx context.Context, intent PreparedSync, branch plumbing.ReferenceName) error {
	actual, err := a.remoteBranch(ctx)
	if err != nil {
		if errors.Is(err, ErrCacheMismatch) || errors.Is(err, ErrBranchMismatch) || errors.Is(err, ErrDefaultBranchUnclear) {
			return err
		}
		return &preparedPushError{cause: err}
	}
	if actual != branch {
		return stalePreparedSync("Git 远端默认分支已变化")
	}
	// Remote discovery can take time. Recheck the worktree and fixed commit
	// before sending; callers must still exclude non-cooperating writers.
	a.mu.Lock()
	err = a.validatePreparedSyncLocked(intent, branch)
	a.mu.Unlock()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	auth, err := a.config.authMethod()
	if err != nil {
		return &preparedPushError{cause: err}
	}
	err = a.repo.PushContext(ctx, &gogit.PushOptions{
		RemoteName: defaultRemoteName,
		RemoteURL:  a.config.URL,
		Auth:       auth,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(intent.Commit + ":" + branch.String())},
	})
	if err == nil || errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil
	}
	return &preparedPushError{cause: mapGitError(err)}
}

// Transport errors can contain the remote URL and its credentials.
type preparedPushError struct{ cause error }

func (e *preparedPushError) Error() string {
	return "Git 推送失败，请检查连接、权限和远端状态"
}
func (e *preparedPushError) Unwrap() error { return e.cause }

type preparedSyncError struct{ cause error }

func (e *preparedSyncError) Error() string {
	return "准备 Git 同步失败，请检查连接、权限和工作区的同步备份"
}
func (e *preparedSyncError) Unwrap() error { return e.cause }

// These wrappers preserve the legacy Sync error while allowing PrepareSync
// to distinguish transport failures from actionable local recovery failures.
type syncPullFailure struct{ cause error }

func (e *syncPullFailure) Error() string { return e.cause.Error() }
func (e *syncPullFailure) Unwrap() error { return e.cause }

type syncBackupRecoveryError struct {
	message string
	path    string
	cause   error
}

func (e *syncBackupRecoveryError) Error() string {
	if e.cause == nil {
		return fmt.Sprintf("%s %s", e.message, e.path)
	}
	return fmt.Sprintf("%s %s: %v", e.message, e.path, e.cause)
}
func (e *syncBackupRecoveryError) Unwrap() error { return e.cause }

type preparedSyncBackupError struct {
	path  string
	cause error
}

func (e *preparedSyncBackupError) Error() string {
	return fmt.Sprintf("Git 同步备份需要处理：%s；请检查并恢复后重试", e.path)
}
func (e *preparedSyncBackupError) Unwrap() error { return e.cause }
