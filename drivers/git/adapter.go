package gitdriver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/local"
	gogit "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type Adapter struct {
	gitMu        sync.Mutex
	mu           sync.Mutex
	config       Config
	repo         *gogit.Repository
	worktree     *gogit.Worktree
	files        *local.Adapter
	capabilities filesystem.CapabilitySet
}

type Status struct {
	Dirty   bool     `json:"dirty"`
	Changed []string `json:"changed"`
}

type commitMessageChanges struct {
	Added    []string
	Modified []string
	Deleted  []string
	Renamed  []string
}

const defaultCommitMessage = "同步文件变更"

type dirtySnapshot struct {
	Path        string
	Exists      bool
	Content     []byte
	BaseExists  bool
	BaseContent []byte
	Untracked   bool
}

func New(ctx context.Context, config Config) (*Adapter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	repo, err := ensureRepository(ctx, config)
	if err != nil {
		return nil, mapGitError(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, mapGitError(err)
	}
	files, err := local.New(local.Config{Root: config.Root, Visibility: config.Visibility})
	if err != nil {
		return nil, err
	}
	adapter := &Adapter{
		config:   config,
		repo:     repo,
		worktree: worktree,
		files:    files,
		capabilities: filesystem.NewCapabilitySet(
			filesystem.CapabilityCopy,
			filesystem.CapabilityMove,
			filesystem.CapabilityDirectory,
		),
	}
	if config.AutoPull {
		if err := adapter.Pull(ctx); err != nil {
			return nil, err
		}
	}
	return adapter, nil
}

func (a *Adapter) Write(ctx context.Context, path string, r io.Reader, opts filesystem.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: nil reader", filesystem.ErrInvalidPath)
	}
	if err := a.ensureWritablePathForWrite(ctx, path); err != nil {
		return err
	}
	spooled, cleanup, err := spoolGitWriteReader(ctx, r)
	if err != nil {
		return err
	}
	defer cleanup()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureWritablePath(ctx, path, false); err != nil {
		return err
	}
	return a.files.Write(ctx, path, spooled, opts)
}

func (a *Adapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(path); err != nil {
		return nil, err
	}
	return a.files.Open(ctx, path)
}

func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureWritablePath(ctx, path, false); err != nil {
		return err
	}
	return a.files.Delete(ctx, path)
}

func (a *Adapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(path); err != nil {
		return false, err
	}
	return a.files.Exists(ctx, path)
}

func (a *Adapter) Stat(ctx context.Context, path string) (filesystem.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.FileInfo{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(path); err != nil {
		return filesystem.FileInfo{}, err
	}
	return a.files.Stat(ctx, path)
}

func (a *Adapter) ListPage(ctx context.Context, prefix string, opts filesystem.ListOptions) (filesystem.Page, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.Page{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(prefix); err != nil && prefix != "" {
		return filesystem.Page{}, err
	}
	if err := ctx.Err(); err != nil {
		return filesystem.Page{}, err
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 1000
	}
	entries, err := a.listEntries(prefix, opts.Recursive)
	if err != nil {
		return filesystem.Page{}, err
	}
	start := 0
	if opts.Cursor != "" {
		for start < len(entries) && entries[start].Path <= opts.Cursor {
			start++
		}
	}
	end := start + pageSize
	page := filesystem.Page{}
	if end < len(entries) {
		page.Items = entries[start:end]
		page.NextCursor = page.Items[len(page.Items)-1].Path
		return page, nil
	}
	if start < len(entries) {
		page.Items = entries[start:]
	}
	return page, nil
}

func (a *Adapter) listEntries(prefix string, recursive bool) ([]filesystem.Entry, error) {
	prefix = strings.Trim(prefix, "/")
	root := a.files.Root()
	base := root
	if prefix != "" {
		base = filepath.Join(root, filepath.FromSlash(prefix))
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return nil, mapLocalStatError(err)
	}
	if resolvedBase != resolvedRoot && !strings.HasPrefix(resolvedBase, resolvedRoot+string(os.PathSeparator)) {
		return nil, fmt.Errorf("%w: path escapes git root", filesystem.ErrInvalidPath)
	}
	info, err := os.Stat(resolvedBase)
	if err != nil {
		return nil, mapLocalStatError(err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: list target is not a directory", filesystem.ErrInvalidPath)
	}
	var entries []filesystem.Entry
	if !recursive {
		children, err := os.ReadDir(resolvedBase)
		if err != nil {
			return nil, mapLocalStatError(err)
		}
		for _, child := range children {
			childPath := pathJoin(prefix, child.Name())
			entry, skip, err := a.entryFromDirEntry(childPath, child)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			entries = append(entries, entry)
		}
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		return entries, nil
	}
	err = filepath.WalkDir(resolvedBase, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return mapLocalStatError(walkErr)
		}
		if current == resolvedBase {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		entryPath := filepath.ToSlash(rel)
		entry, skip, err := a.entryFromDirEntry(entryPath, d)
		if err != nil {
			return err
		}
		if skip {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (a *Adapter) entryFromDirEntry(entryPath string, dirEntry os.DirEntry) (filesystem.Entry, bool, error) {
	if isGitInternalPath(entryPath) {
		return filesystem.Entry{}, true, nil
	}
	isDir := dirEntry.IsDir()
	if ignored, _ := a.isIgnored(entryPath, isDir); ignored {
		return filesystem.Entry{}, true, nil
	}
	info, err := dirEntry.Info()
	if err != nil {
		return filesystem.Entry{}, false, mapLocalStatError(err)
	}
	entryType := filesystem.EntryFile
	size := info.Size()
	if info.IsDir() {
		entryType = filesystem.EntryDirectory
		size = 0
	}
	return filesystem.Entry{
		Path:         entryPath,
		Type:         entryType,
		Size:         size,
		LastModified: info.ModTime(),
	}, false, nil
}

func pathJoin(prefix string, name string) string {
	if prefix == "" {
		return name
	}
	return strings.Trim(prefix, "/") + "/" + name
}

func mapLocalStatError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return filesystem.ErrNotFound
	}
	if errors.Is(err, os.ErrPermission) {
		return filesystem.ErrReadOnly
	}
	return err
}

func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	if a.config.ReadOnly {
		return a.capabilities.Without(filesystem.CapabilityCopy, filesystem.CapabilityMove, filesystem.CapabilityDirectory)
	}
	return a.capabilities.Clone()
}

func (a *Adapter) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(src); err != nil {
		return err
	}
	if err := a.ensureWritablePath(ctx, dst, false); err != nil {
		return err
	}
	return a.files.Copy(ctx, src, dst)
}

func (a *Adapter) Move(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := rejectGitInternalPath(src); err != nil {
		return err
	}
	if err := a.ensureWritablePath(ctx, dst, false); err != nil {
		return err
	}
	return a.files.Move(ctx, src, dst)
}

func (a *Adapter) MakeDirectory(ctx context.Context, dir string, opts filesystem.DirectoryOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureWritablePath(ctx, dir, true); err != nil {
		return err
	}
	return a.files.MakeDirectory(ctx, dir, opts)
}

func (a *Adapter) DeleteDirectory(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.ensureWritablePath(ctx, dir, true); err != nil {
		return err
	}
	return a.files.DeleteDirectory(ctx, dir)
}

func (a *Adapter) DirectorySemantics() filesystem.DirectorySemantics {
	return filesystem.DirectoryReal
}

func (a *Adapter) Pull(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.gitMu.Lock()
	defer a.gitMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.pullLocked(ctx)
}

func (a *Adapter) pullLocked(ctx context.Context) error {
	auth, err := a.config.authMethod()
	if err != nil {
		return err
	}
	err = a.worktree.PullContext(ctx, &gogit.PullOptions{
		RemoteName:    defaultRemoteName,
		RemoteURL:     a.config.URL,
		ReferenceName: branchReferenceName(a.config.Branch),
		SingleBranch:  a.config.Branch != "",
		Auth:          auth,
	})
	if err == nil || errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return a.pruneEmptyDirectoriesLocked()
	}
	if isEmptyRepositoryError(err) {
		return a.pruneEmptyDirectoriesLocked()
	}
	return mapGitError(err)
}

func (a *Adapter) Commit(ctx context.Context, message string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.config.ReadOnly {
		return "", filesystem.ErrReadOnly
	}
	message = strings.TrimSpace(message)
	a.gitMu.Lock()
	defer a.gitMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commitLocked(message)
}

func (a *Adapter) commitLocked(message string) (string, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		generated, err := a.defaultCommitMessageLocked()
		if err != nil {
			return "", err
		}
		message = generated
	}
	if err := a.worktree.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		return "", mapGitError(err)
	}
	hash, err := a.worktree.Commit(message, &gogit.CommitOptions{
		All: true,
		Author: &object.Signature{
			Name:  a.config.CommitName,
			Email: a.config.CommitEmail,
			When:  time.Now(),
		},
	})
	if errors.Is(err, gogit.ErrEmptyCommit) {
		return "", nil
	}
	if err != nil {
		return "", mapGitError(err)
	}
	return hash.String(), nil
}

func (a *Adapter) Push(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.config.ReadOnly {
		return filesystem.ErrReadOnly
	}
	a.gitMu.Lock()
	defer a.gitMu.Unlock()
	return a.pushLocked(ctx)
}

func (a *Adapter) pushLocked(ctx context.Context) error {
	auth, err := a.config.authMethod()
	if err != nil {
		return err
	}
	refSpec := gitconfig.RefSpec("")
	if a.config.Branch != "" {
		name := branchReferenceName(a.config.Branch)
		refSpec = gitconfig.RefSpec(fmt.Sprintf("%s:%s", name, name))
	}
	options := &gogit.PushOptions{
		RemoteName: defaultRemoteName,
		RemoteURL:  a.config.URL,
		Auth:       auth,
	}
	if refSpec != "" {
		options.RefSpecs = []gitconfig.RefSpec{refSpec}
	}
	err = a.repo.PushContext(ctx, options)
	if err == nil || errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil
	}
	return mapGitError(err)
}

func (a *Adapter) Sync(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.config.ReadOnly {
		return filesystem.ErrReadOnly
	}
	message = strings.TrimSpace(message)
	a.gitMu.Lock()
	defer a.gitMu.Unlock()
	a.mu.Lock()

	snapshots, err := a.snapshotDirtyLocked()
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if len(snapshots) > 0 {
		if err := a.cleanWorktreeLocked(snapshots); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	if err := a.pullLocked(ctx); err != nil {
		a.mu.Unlock()
		return err
	}
	if len(snapshots) > 0 {
		if err := a.restoreSnapshotsLocked(snapshots, time.Now()); err != nil {
			a.mu.Unlock()
			return err
		}
	}
	if err := a.pruneEmptyDirectoriesLocked(); err != nil {
		a.mu.Unlock()
		return err
	}
	if _, err := a.commitLocked(message); err != nil {
		a.mu.Unlock()
		return err
	}
	a.mu.Unlock()
	return a.pushLocked(ctx)
}

func (a *Adapter) Status(ctx context.Context) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	status, err := a.worktree.StatusWithOptions(gogit.StatusOptions{Strategy: gogit.Preload})
	if err != nil {
		return Status{}, mapGitError(err)
	}
	changed := make([]string, 0, len(status))
	for path, file := range status {
		if file.Worktree == gogit.Unmodified && file.Staging == gogit.Unmodified {
			continue
		}
		if isGitInternalPath(path) {
			continue
		}
		changed = append(changed, path)
	}
	sort.Strings(changed)
	return Status{Dirty: len(changed) > 0, Changed: changed}, nil
}

func (a *Adapter) defaultCommitMessageLocked() (string, error) {
	status, err := a.worktree.StatusWithOptions(gogit.StatusOptions{Strategy: gogit.Preload})
	if err != nil {
		return "", mapGitError(err)
	}
	changes := commitMessageChanges{}
	for filePath, file := range status {
		if file.Worktree == gogit.Unmodified && file.Staging == gogit.Unmodified {
			continue
		}
		if isGitInternalPath(filePath) {
			continue
		}
		switch classifyCommitMessageChange(file) {
		case "added":
			changes.Added = append(changes.Added, filePath)
		case "deleted":
			changes.Deleted = append(changes.Deleted, filePath)
		case "renamed":
			changes.Renamed = append(changes.Renamed, renameCommitMessagePath(filePath, file.Extra))
		default:
			changes.Modified = append(changes.Modified, filePath)
		}
	}
	return formatCommitMessage(changes), nil
}

func classifyCommitMessageChange(file *gogit.FileStatus) string {
	if file == nil {
		return "modified"
	}
	if file.Worktree == gogit.Renamed || file.Staging == gogit.Renamed {
		return "renamed"
	}
	if file.Worktree == gogit.Untracked || file.Staging == gogit.Untracked || file.Staging == gogit.Added || file.Worktree == gogit.Added || file.Staging == gogit.Copied || file.Worktree == gogit.Copied {
		return "added"
	}
	if file.Worktree == gogit.Deleted || file.Staging == gogit.Deleted {
		return "deleted"
	}
	return "modified"
}

func renameCommitMessagePath(filePath string, previous string) string {
	previous = strings.TrimSpace(previous)
	if previous == "" {
		return filePath
	}
	return previous + " -> " + filePath
}

func formatCommitMessage(changes commitMessageChanges) string {
	sort.Strings(changes.Added)
	sort.Strings(changes.Modified)
	sort.Strings(changes.Deleted)
	sort.Strings(changes.Renamed)
	total := len(changes.Added) + len(changes.Modified) + len(changes.Deleted) + len(changes.Renamed)
	if total == 0 {
		return defaultCommitMessage
	}
	if total == 1 {
		if len(changes.Added) == 1 {
			return "新增 " + changes.Added[0]
		}
		if len(changes.Modified) == 1 {
			return "更新 " + changes.Modified[0]
		}
		if len(changes.Deleted) == 1 {
			return "删除 " + changes.Deleted[0]
		}
		return "重命名 " + changes.Renamed[0]
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "同步 %d 个文件变更", total)
	appendCommitMessageSection(&builder, "新增", changes.Added)
	appendCommitMessageSection(&builder, "更新", changes.Modified)
	appendCommitMessageSection(&builder, "删除", changes.Deleted)
	appendCommitMessageSection(&builder, "重命名", changes.Renamed)
	return builder.String()
}

func appendCommitMessageSection(builder *strings.Builder, title string, paths []string) {
	if len(paths) == 0 {
		return
	}
	_, _ = fmt.Fprintf(builder, "\n\n%s:", title)
	for _, filePath := range paths {
		_, _ = fmt.Fprintf(builder, "\n- %s", filePath)
	}
}

func (a *Adapter) Conflicts(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	root := a.files.Root()
	var conflicts []string
	err := filepath.WalkDir(root, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return mapLocalStatError(walkErr)
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		entryPath := filepath.ToSlash(rel)
		if isGitInternalPath(entryPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if isConflictCopyName(entryPath) {
			conflicts = append(conflicts, entryPath)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(conflicts)
	return conflicts, nil
}

func (a *Adapter) ensureWritablePath(ctx context.Context, path string, isDir bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.config.ReadOnly {
		return filesystem.ErrReadOnly
	}
	if err := rejectGitInternalPath(path); err != nil {
		return err
	}
	if ignored, err := a.isIgnored(path, isDir); err != nil {
		return err
	} else if ignored {
		return fmt.Errorf("%w: path is ignored by .gitignore", filesystem.ErrInvalidPath)
	}
	return nil
}

func (a *Adapter) ensureWritablePathForWrite(ctx context.Context, path string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ensureWritablePath(ctx, path, false)
}

func spoolGitWriteReader(ctx context.Context, r io.Reader) (io.Reader, func(), error) {
	tmp, err := os.CreateTemp("", "go-filesystem-git-write-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		name := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, err
	}
	return tmp, cleanup, nil
}

func (a *Adapter) isIgnored(path string, isDir bool) (bool, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return false, nil
	}
	patterns, err := gitignore.ReadPatterns(a.worktree.Filesystem, nil)
	if err != nil {
		return false, nil
	}
	patterns = append(patterns, a.worktree.Excludes...)
	if len(patterns) == 0 {
		return false, nil
	}
	segments := strings.Split(filepath.ToSlash(path), "/")
	return gitignore.NewMatcher(patterns).Match(segments, isDir), nil
}

func ensureRepository(ctx context.Context, config Config) (*gogit.Repository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gitDir := filepath.Join(config.Root, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return gogit.PlainOpen(config.Root)
	}
	if err := ensureCloneTarget(config.Root); err != nil {
		return nil, err
	}
	auth, err := config.authMethod()
	if err != nil {
		return nil, err
	}
	repo, err := gogit.PlainCloneContext(ctx, config.Root, false, &gogit.CloneOptions{
		URL:           config.URL,
		Auth:          auth,
		RemoteName:    defaultRemoteName,
		ReferenceName: branchReferenceName(config.Branch),
		SingleBranch:  config.Branch != "",
	})
	if err == nil {
		return repo, nil
	}
	if !isEmptyRepositoryError(err) {
		return nil, err
	}
	repo, err = gogit.PlainInit(config.Root, false)
	if err != nil {
		return nil, err
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: defaultRemoteName, URLs: []string{config.URL}}); err != nil {
		return nil, err
	}
	if config.Branch != "" {
		if err := setHeadBranch(repo, config.Branch); err != nil {
			return nil, err
		}
	}
	return repo, nil
}

func setHeadBranch(repo *gogit.Repository, branch string) error {
	branchRef := branchReferenceName(branch)
	if branchRef == "" {
		return nil
	}
	return repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branchRef))
}

func (a *Adapter) snapshotDirtyLocked() ([]dirtySnapshot, error) {
	status, err := a.worktree.StatusWithOptions(gogit.StatusOptions{Strategy: gogit.Preload})
	if err != nil {
		return nil, mapGitError(err)
	}
	paths := make([]string, 0, len(status))
	for path, file := range status {
		if file.Worktree == gogit.Unmodified && file.Staging == gogit.Unmodified {
			continue
		}
		if isGitInternalPath(path) {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	snapshots := make([]dirtySnapshot, 0, len(paths))
	for _, path := range paths {
		file := status[path]
		snapshot := dirtySnapshot{
			Path:      path,
			Untracked: file.Worktree == gogit.Untracked || file.Staging == gogit.Untracked,
		}
		baseExists, baseContent, err := a.headFileContentLocked(path)
		if err != nil {
			return nil, err
		}
		snapshot.BaseExists = baseExists
		snapshot.BaseContent = baseContent
		content, err := os.ReadFile(filepath.Join(a.files.Root(), filepath.FromSlash(path)))
		if err == nil {
			snapshot.Exists = true
			snapshot.Content = content
		} else if !errors.Is(err, os.ErrNotExist) {
			info, statErr := os.Stat(filepath.Join(a.files.Root(), filepath.FromSlash(path)))
			if statErr == nil && info.IsDir() {
				continue
			}
			return nil, mapLocalStatError(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (a *Adapter) headFileContentLocked(path string) (bool, []byte, error) {
	head, err := a.repo.Head()
	if err != nil {
		if isReferenceNotFoundError(err) {
			return false, nil, nil
		}
		return false, nil, mapGitError(err)
	}
	commit, err := a.repo.CommitObject(head.Hash())
	if err != nil {
		return false, nil, mapGitError(err)
	}
	file, err := commit.File(path)
	if err != nil {
		if errors.Is(err, object.ErrFileNotFound) {
			return false, nil, nil
		}
		return false, nil, mapGitError(err)
	}
	content, err := file.Contents()
	if err != nil {
		return false, nil, mapGitError(err)
	}
	return true, []byte(content), nil
}

func (a *Adapter) cleanWorktreeLocked(snapshots []dirtySnapshot) error {
	if _, err := a.repo.Head(); err == nil {
		if err := a.worktree.Reset(&gogit.ResetOptions{Mode: gogit.HardReset}); err != nil {
			return mapGitError(err)
		}
	} else if !isReferenceNotFoundError(err) {
		return mapGitError(err)
	}
	for _, snapshot := range snapshots {
		if !snapshot.Untracked {
			continue
		}
		if err := os.RemoveAll(filepath.Join(a.files.Root(), filepath.FromSlash(snapshot.Path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (a *Adapter) restoreSnapshotsLocked(snapshots []dirtySnapshot, at time.Time) error {
	for _, snapshot := range snapshots {
		target := filepath.Join(a.files.Root(), filepath.FromSlash(snapshot.Path))
		remoteContent, remoteErr := os.ReadFile(target)
		remoteExists := remoteErr == nil
		if remoteErr != nil && !errors.Is(remoteErr, os.ErrNotExist) {
			return mapLocalStatError(remoteErr)
		}
		if snapshot.Exists && remoteChangedSinceSnapshot(snapshot, remoteExists, remoteContent) {
			conflictPath := ConflictFileName(snapshot.Path, at)
			conflictTarget := filepath.Join(a.files.Root(), filepath.FromSlash(conflictPath))
			if err := os.MkdirAll(filepath.Dir(conflictTarget), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(conflictTarget, remoteContent, 0o600); err != nil {
				return err
			}
		}
		if !snapshot.Exists {
			if remoteChangedSinceSnapshot(snapshot, remoteExists, remoteContent) {
				conflictPath := ConflictFileName(snapshot.Path, at)
				conflictTarget := filepath.Join(a.files.Root(), filepath.FromSlash(conflictPath))
				if err := os.MkdirAll(filepath.Dir(conflictTarget), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(conflictTarget, remoteContent, 0o600); err != nil {
					return err
				}
			}
			if err := os.RemoveAll(target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, snapshot.Content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func remoteChangedSinceSnapshot(snapshot dirtySnapshot, remoteExists bool, remoteContent []byte) bool {
	if snapshot.BaseExists != remoteExists {
		return true
	}
	if !snapshot.BaseExists {
		return false
	}
	return !bytes.Equal(remoteContent, snapshot.BaseContent)
}

func (a *Adapter) pruneEmptyDirectoriesLocked() error {
	root := a.files.Root()
	var dirs []string
	err := filepath.WalkDir(root, func(current string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return mapLocalStatError(walkErr)
		}
		if current == root {
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		entryPath := filepath.ToSlash(rel)
		if isGitInternalPath(entryPath) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, current)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j])
	})
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return mapLocalStatError(err)
		}
		if len(entries) > 0 {
			continue
		}
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return mapLocalStatError(err)
		}
	}
	return nil
}

func ensureCloneTarget(root string) error {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: git cache root is not an empty repository", filesystem.ErrInvalidPath)
	}
	return nil
}

func branchReferenceName(branch string) plumbing.ReferenceName {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return ""
	}
	return plumbing.NewBranchReferenceName(branch)
}

func rejectGitInternalPath(path string) error {
	if isGitInternalPath(path) {
		return fmt.Errorf("%w: .git directory is not exposed", filesystem.ErrInvalidPath)
	}
	return nil
}

func isGitInternalPath(path string) bool {
	path = strings.Trim(filepath.ToSlash(path), "/")
	return path == ".git" || strings.HasPrefix(path, ".git/")
}

func isReferenceNotFoundError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "reference not found")
}

func mapGitError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, filesystem.ErrInvalidPath) || errors.Is(err, filesystem.ErrReadOnly) {
		return err
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "authentication") || strings.Contains(message, "authorization") || strings.Contains(message, "permission denied"):
		return fmt.Errorf("%w: git authentication failed: %v", filesystem.ErrReadOnly, err)
	case strings.Contains(message, "not found") || strings.Contains(message, "repository not found"):
		return fmt.Errorf("%w: git repository not found: %v", filesystem.ErrNotFound, err)
	case strings.Contains(message, "already exists"):
		return fmt.Errorf("%w: %v", filesystem.ErrAlreadyExists, err)
	default:
		return err
	}
}
