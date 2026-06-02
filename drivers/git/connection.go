package gitdriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage/memory"
)

type AccessMode string

const (
	AccessModeReadOnly    AccessMode = "read_only"
	AccessModeReadWrite   AccessMode = "read_write"
	AccessModeUnavailable AccessMode = "unavailable"
)

type ConnectionTestResult struct {
	URL      URLInfo    `json:"url"`
	CanRead  bool       `json:"can_read"`
	CanWrite bool       `json:"can_write"`
	Mode     AccessMode `json:"mode"`
	Message  string     `json:"message"`
}

func TestConnection(ctx context.Context, config Config) (ConnectionTestResult, error) {
	if err := ctx.Err(); err != nil {
		return ConnectionTestResult{}, err
	}
	info, err := ParseURL(config.URL)
	if err != nil {
		return ConnectionTestResult{}, err
	}
	config.URL = strings.TrimSpace(config.URL)
	config.Branch = strings.TrimSpace(config.Branch)
	config.AuthMode = normalizeAuthMode(config.AuthMode, config.PrivateKey, config.Password)
	config.Username = strings.TrimSpace(config.Username)
	config.Password = strings.TrimSpace(config.Password)
	config.PrivateKey = strings.TrimSpace(config.PrivateKey)
	config.PrivateKeyPassphrase = strings.TrimSpace(config.PrivateKeyPassphrase)
	auth, err := config.authMethod()
	if err != nil {
		return ConnectionTestResult{}, err
	}
	remote := gogit.NewRemote(memory.NewStorage(), &gitconfig.RemoteConfig{
		Name: defaultRemoteName,
		URLs: []string{config.URL},
	})
	_, err = remote.ListContext(ctx, &gogit.ListOptions{Auth: auth})
	if err != nil {
		return ConnectionTestResult{
			URL:     info,
			Mode:    AccessModeUnavailable,
			Message: fmt.Sprintf("无法读取远端仓库：%s", err.Error()),
		}, nil
	}
	canWrite := false
	mode := AccessModeReadOnly
	message := "远端仓库可读取，将按只读模式接入"
	if config.hasWriteCredential() {
		canWrite, err = probeWritePermission(ctx, config, auth)
		if canWrite {
			mode = AccessModeReadWrite
			message = "远端仓库可读取，写入权限探测通过，将按可写模式接入"
		} else if err != nil {
			message = fmt.Sprintf("远端仓库可读取，但写入权限探测失败，将按只读模式接入：%s", err.Error())
		}
	}
	return ConnectionTestResult{
		URL:      info,
		CanRead:  true,
		CanWrite: canWrite,
		Mode:     mode,
		Message:  message,
	}, nil
}

func probeWritePermission(ctx context.Context, config Config, auth transport.AuthMethod) (bool, error) {
	root, err := os.MkdirTemp("", "ll-filebrowser-git-probe-*")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(root)

	repo, err := gogit.PlainCloneContext(ctx, root, false, &gogit.CloneOptions{
		URL:           config.URL,
		Auth:          auth,
		RemoteName:    defaultRemoteName,
		ReferenceName: branchReferenceName(config.Branch),
		SingleBranch:  config.Branch != "",
		Depth:         1,
	})
	if err != nil {
		if !isEmptyRepositoryError(err) {
			return false, err
		}
		repo, err = gogit.PlainInit(root, false)
		if err != nil {
			return false, err
		}
		if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: defaultRemoteName, URLs: []string{config.URL}}); err != nil {
			return false, err
		}
	}
	refName := plumbing.NewBranchReferenceName(fmt.Sprintf("ll-filebrowser-probe-%d", time.Now().UnixNano()))
	hash, err := ensureProbeCommit(repo, config)
	if err != nil {
		return false, err
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		return false, err
	}
	refSpec := gitconfig.RefSpec(fmt.Sprintf("%s:%s", refName, refName))
	if err := repo.PushContext(ctx, &gogit.PushOptions{
		RemoteName: defaultRemoteName,
		RemoteURL:  config.URL,
		RefSpecs:   []gitconfig.RefSpec{refSpec},
		Auth:       auth,
	}); err != nil {
		return false, err
	}
	deleteSpec := gitconfig.RefSpec(fmt.Sprintf(":%s", refName))
	if err := repo.PushContext(ctx, &gogit.PushOptions{
		RemoteName: defaultRemoteName,
		RemoteURL:  config.URL,
		RefSpecs:   []gitconfig.RefSpec{deleteSpec},
		Auth:       auth,
	}); err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return true, fmt.Errorf("临时写入探测分支清理失败：%w", err)
	}
	return true, nil
}

func ensureProbeCommit(repo *gogit.Repository, config Config) (plumbing.Hash, error) {
	head, err := repo.Head()
	if err == nil {
		return head.Hash(), nil
	}
	worktree, worktreeErr := repo.Worktree()
	if worktreeErr != nil {
		return plumbing.ZeroHash, worktreeErr
	}
	if err := os.WriteFile(filepath.Join(worktree.Filesystem.Root(), "ll-filebrowser-write-probe.txt"), []byte("write probe\n"), 0o600); err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := worktree.Add("ll-filebrowser-write-probe.txt"); err != nil {
		return plumbing.ZeroHash, err
	}
	name := strings.TrimSpace(config.CommitName)
	if name == "" {
		name = "ll-filebrowser"
	}
	email := strings.TrimSpace(config.CommitEmail)
	if email == "" {
		email = "ll-filebrowser@example.local"
	}
	hash, err := worktree.Commit("ll-filebrowser 写入权限探测", &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  name,
			Email: email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return hash, nil
}

func isEmptyRepositoryError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "remote repository is empty") ||
		strings.Contains(message, "reference not found") ||
		strings.Contains(message, "couldn't find remote ref")
}

func ConflictFileName(filePath string, at time.Time) string {
	filePath = strings.TrimSpace(filePath)
	dir := path.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	name := path.Base(filePath)
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	if stem == "" {
		stem = name
		ext = ""
	}
	conflictName := stem + "（冲突文件-" + at.Format("20060102-1504") + "）" + ext
	if dir == "" {
		return conflictName
	}
	return path.Join(dir, conflictName)
}

func IsReadOnlyMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(AccessModeReadOnly), "readonly", "read-only":
		return true
	case string(AccessModeReadWrite), "readwrite", "read-write", "writable":
		return false
	default:
		return true
	}
}

func NormalizeAccessMode(mode string, hasCredential bool) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(AccessModeReadWrite), "readwrite", "read-write", "writable":
		return string(AccessModeReadWrite)
	case string(AccessModeReadOnly), "readonly", "read-only":
		return string(AccessModeReadOnly)
	default:
		if hasCredential {
			return string(AccessModeReadWrite)
		}
		return string(AccessModeReadOnly)
	}
}

func accessModeValidationError() error {
	return fmt.Errorf("%w: git access mode is invalid", filesystem.ErrInvalidPath)
}
