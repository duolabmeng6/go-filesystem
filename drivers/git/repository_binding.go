package gitdriver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
)

// ErrCacheMismatch means the cached worktree is bound to a different remote
// or was replaced after it was opened. The cache is never cleared implicitly.
var ErrCacheMismatch = errors.New("git cache does not match the configured repository")

// ErrBranchMismatch means the cached worktree is not on the intended branch.
var ErrBranchMismatch = errors.New("git cache is not on the configured branch")

// ErrDefaultBranchUnclear means the remote did not advertise a usable HEAD.
var ErrDefaultBranchUnclear = errors.New("git remote default branch is not confirmed")

// validateRepositoryBinding prevents a previously cloned cache from being
// silently reused for a different repository or an incompatible branch.
func validateRepositoryBinding(repo *gogit.Repository, config Config) error {
	remote, err := repo.Remote(defaultRemoteName)
	if err != nil {
		return fmt.Errorf("%w: %w: Git 缓存缺少 origin，请检查缓存目录", filesystem.ErrInvalidPath, ErrCacheMismatch)
	}
	urls := remote.Config().URLs
	if len(urls) != 1 || gitRemoteIdentity(urls[0]) != gitRemoteIdentity(config.URL) {
		return fmt.Errorf("%w: %w: Git 缓存对应的仓库地址与当前配置不一致，请检查缓存目录", filesystem.ErrInvalidPath, ErrCacheMismatch)
	}
	head, err := repo.Storer.Reference(plumbing.HEAD)
	if err != nil {
		return fmt.Errorf("%w: %w: 无法读取 Git 缓存当前分支", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	if head.Type() != plumbing.SymbolicReference || !head.Target().IsBranch() {
		return fmt.Errorf("%w: %w: Git 缓存未处于分支，请先检查工作区", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	if config.Branch != "" && head.Target() != branchReferenceName(config.Branch) {
		return fmt.Errorf("%w: %w: Git 缓存当前分支与配置不一致，请检查工作区", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	return nil
}

func (a *Adapter) validateRepositoryBinding() error {
	root, err := os.Stat(a.files.Root())
	if err != nil || a.rootIdentity == nil || !os.SameFile(root, a.rootIdentity) {
		return fmt.Errorf("%w: %w: Git 工作区已更换，请重新打开储存器", filesystem.ErrInvalidPath, ErrCacheMismatch)
	}
	gitDir, err := os.Stat(filepath.Join(a.files.Root(), ".git"))
	if err != nil || a.gitIdentity == nil || !os.SameFile(gitDir, a.gitIdentity) {
		return fmt.Errorf("%w: %w: Git 仓库已更换，请重新打开储存器", filesystem.ErrInvalidPath, ErrCacheMismatch)
	}
	return validateRepositoryBinding(a.repo, a.config)
}

// branchCommitLocked freezes the source commit while the worktree is locked.
// The network push can then proceed without blocking ordinary file operations.
func (a *Adapter) branchCommitLocked(branch plumbing.ReferenceName) (plumbing.Hash, error) {
	if err := a.validateRepositoryBinding(); err != nil {
		return plumbing.ZeroHash, err
	}
	head, err := a.repo.Storer.Reference(plumbing.HEAD)
	if err != nil || head.Type() != plumbing.SymbolicReference || head.Target() != branch {
		return plumbing.ZeroHash, fmt.Errorf("%w: %w: Git 当前分支发生变化，请检查工作区", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	ref, err := a.repo.Reference(branch, true)
	if err != nil || ref.Hash().IsZero() {
		return plumbing.ZeroHash, fmt.Errorf("%w: %w: Git 当前分支没有可推送的提交", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	if _, err := a.repo.CommitObject(ref.Hash()); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("%w: %w: 无法读取 Git 当前提交", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	return ref.Hash(), nil
}

// gitRemoteIdentity ignores the password, which may rotate independently of
// the repository identity. Query parameters are retained: they may select a
// different repository, even though this means token rotations need a review.
func gitRemoteIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if !urlSchemePattern.MatchString(raw) {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		parsed.User = url.User(parsed.User.Username())
	}
	return parsed.String()
}

func (a *Adapter) remoteBranch(ctx context.Context) (plumbing.ReferenceName, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := a.validateRepositoryBinding(); err != nil {
		return "", err
	}
	head, err := a.repo.Storer.Reference(plumbing.HEAD)
	if err != nil {
		return "", err
	}
	if a.config.Branch != "" {
		return head.Target(), nil
	}
	branch, err := a.remoteDefaultBranch(ctx)
	if err != nil {
		return "", err
	}
	if head.Target() != branch {
		return "", fmt.Errorf("%w: %w: Git 缓存当前分支与远端默认分支不一致，请检查工作区", filesystem.ErrInvalidPath, ErrBranchMismatch)
	}
	return branch, nil
}

// remoteDefaultBranch reads the advertised symref directly: go-git may infer
// HEAD from matching hashes when the server does not advertise a default.
func (a *Adapter) remoteDefaultBranch(ctx context.Context) (plumbing.ReferenceName, error) {
	endpoint, err := transport.NewEndpoint(a.config.URL)
	if err != nil {
		return "", fmt.Errorf("%w: Git 仓库地址无效", filesystem.ErrInvalidPath)
	}
	transportClient, err := client.NewClient(endpoint)
	if err != nil {
		return "", safeRemoteDiscoveryError(err)
	}
	auth, err := a.config.authMethod()
	if err != nil {
		return "", err
	}
	session, err := transportClient.NewUploadPackSession(endpoint, auth)
	if err != nil {
		return "", safeRemoteDiscoveryError(err)
	}
	refs, listErr := session.AdvertisedReferencesContext(ctx)
	closeErr := session.Close()
	if listErr != nil {
		if isEmptyRepositoryError(listErr) {
			return "", fmt.Errorf("%w: %w: 远端没有默认分支，请在配置中指定分支", filesystem.ErrInvalidPath, ErrDefaultBranchUnclear)
		}
		return "", safeRemoteDiscoveryError(errors.Join(listErr, closeErr))
	}
	if closeErr != nil {
		return "", safeRemoteDiscoveryError(closeErr)
	}
	return advertisedDefaultBranch(refs)
}

type gitRemoteDiscoveryError struct {
	message string
	cause   error
}

func (e *gitRemoteDiscoveryError) Error() string { return e.message }
func (e *gitRemoteDiscoveryError) Unwrap() error { return e.cause }

// A remote error can embed credentials in URLs or in server responses. Keep
// its chain for error classification without placing it in the displayed text.
func safeRemoteDiscoveryError(err error) error {
	mapped := mapGitError(err)
	message := "git remote default branch discovery failed"
	if errors.Is(mapped, filesystem.ErrReadOnly) {
		message = "git authentication failed"
	} else if errors.Is(mapped, filesystem.ErrNotFound) {
		message = "git repository not found"
	}
	return &gitRemoteDiscoveryError{message: message, cause: errors.Join(mapped, err)}
}

func advertisedDefaultBranch(refs *packp.AdvRefs) (plumbing.ReferenceName, error) {
	if refs == nil || refs.Capabilities == nil || refs.Head == nil {
		return "", fmt.Errorf("%w: %w: 无法确认远端默认分支，请在配置中指定分支", filesystem.ErrInvalidPath, ErrDefaultBranchUnclear)
	}
	var branch plumbing.ReferenceName
	for _, value := range refs.Capabilities.Get(capability.SymRef) {
		name, target, ok := strings.Cut(value, ":")
		if !ok || name != "HEAD" {
			continue
		}
		candidate := plumbing.ReferenceName(target)
		if !candidate.IsBranch() || (branch != "" && branch != candidate) {
			return "", fmt.Errorf("%w: %w: 无法确认远端默认分支，请在配置中指定分支", filesystem.ErrInvalidPath, ErrDefaultBranchUnclear)
		}
		branch = candidate
	}
	if hash, exists := refs.References[branch.String()]; branch != "" && exists && hash == *refs.Head {
		return branch, nil
	}
	return "", fmt.Errorf("%w: %w: 无法确认远端默认分支，请在配置中指定分支", filesystem.ErrInvalidPath, ErrDefaultBranchUnclear)
}
