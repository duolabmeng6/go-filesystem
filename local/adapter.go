package local

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

const defaultPageSize = 1000

var errStopWalk = errors.New("stop walk")

type Adapter struct {
	root                string
	baseURL             string
	urlEnabled          bool
	visibility          filesystem.Visibility
	permissions         Permissions
	temporaryURLBuilder TemporaryURLBuilder
	capabilities        filesystem.CapabilitySet
}

func New(config Config) (*Adapter, error) {
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(config.Root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absRoot, config.Permissions.DirPrivate); err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: root is not a directory", filesystem.ErrInvalidPath)
	}
	caps := filesystem.NewCapabilitySet(
		filesystem.CapabilityCopy,
		filesystem.CapabilityMove,
		filesystem.CapabilityDirectory,
		filesystem.CapabilityVisibility,
	)
	if config.BaseURL != "" {
		caps[filesystem.CapabilityURL] = struct{}{}
	}
	if config.TemporaryURLBuilder != nil {
		caps[filesystem.CapabilityTemporaryURL] = struct{}{}
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	return &Adapter{
		root:                resolvedRoot,
		baseURL:             baseURL,
		urlEnabled:          config.BaseURL != "",
		visibility:          config.Visibility,
		permissions:         config.Permissions,
		temporaryURLBuilder: config.TemporaryURLBuilder,
		capabilities:        caps,
	}, nil
}

func (a *Adapter) Root() string {
	return a.root
}

func (a *Adapter) Write(ctx context.Context, path string, r io.Reader, opts filesystem.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: nil reader", filesystem.ErrInvalidPath)
	}
	if !opts.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	target, err := a.targetPath(path)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := a.checkExistingPrefix(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, a.dirMode(visibility)); err != nil {
		return mapLocalError(err)
	}
	if err := a.checkExistingNoSymlink(parent); err != nil {
		return err
	}
	if err := a.ensureTargetWritable(target, opts.Overwrite); err != nil {
		return err
	}
	if opts.AtomicWrite {
		return a.atomicWrite(ctx, target, r, visibility)
	}
	return a.directWrite(ctx, target, r, visibility, opts.Overwrite)
}

func (a *Adapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	target, err := a.existingPath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return nil, mapLocalError(err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%w: cannot open directory", filesystem.ErrUnsupported)
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, mapLocalError(err)
	}
	return file, nil
}

func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := a.existingPath(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return mapLocalError(err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: delete file does not remove directories", filesystem.ErrUnsupported)
	}
	if err := os.Remove(target); err != nil {
		return mapLocalError(err)
	}
	return nil
}

func (a *Adapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	target, err := a.targetPath(path)
	if err != nil {
		return false, err
	}
	err = a.checkExistingNoSymlink(target)
	if errors.Is(err, filesystem.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) Stat(ctx context.Context, path string) (filesystem.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.FileInfo{}, err
	}
	target, err := a.existingPath(path)
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return filesystem.FileInfo{}, mapLocalError(err)
	}
	return filesystem.FileInfo{
		Path:         path,
		Size:         info.Size(),
		LastModified: info.ModTime(),
		IsDir:        info.IsDir(),
	}, nil
}

func (a *Adapter) ListPage(ctx context.Context, prefix string, opts filesystem.ListOptions) (filesystem.Page, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.Page{}, err
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	cursor, err := decodeCursor(opts.Cursor)
	if err != nil {
		return filesystem.Page{}, err
	}
	dir, displayPrefix, err := a.listRoot(prefix)
	if err != nil {
		return filesystem.Page{}, err
	}
	var items []filesystem.Entry
	if opts.Recursive {
		items, err = a.listRecursive(ctx, dir, displayPrefix, cursor, pageSize)
	} else {
		items, err = a.listFlat(ctx, dir, displayPrefix, cursor, pageSize)
	}
	if err != nil {
		return filesystem.Page{}, err
	}
	page := filesystem.Page{Items: items}
	if len(items) > pageSize {
		page.Items = items[:pageSize]
		page.NextCursor = encodeCursor(page.Items[len(page.Items)-1].Path)
	}
	return page, nil
}

func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	return a.capabilities.Clone()
}

func (a *Adapter) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rc, err := a.Open(ctx, src)
	if err != nil {
		return err
	}
	defer rc.Close()
	return a.Write(ctx, dst, rc, filesystem.DefaultWriteOptions())
}

func (a *Adapter) Move(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := a.existingPath(src)
	if err != nil {
		return err
	}
	target, err := a.targetPath(dst)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	if err := a.checkExistingPrefix(parent); err != nil {
		return err
	}
	if err := os.MkdirAll(parent, a.dirMode(a.visibility)); err != nil {
		return mapLocalError(err)
	}
	if err := a.checkExistingNoSymlink(parent); err != nil {
		return err
	}
	if err := a.ensureTargetWritable(target, true); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return filesystem.ErrNotFound
		}
		return mapLocalError(err)
	}
	return nil
}

func (a *Adapter) MakeDirectory(ctx context.Context, dir string, opts filesystem.DirectoryOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	target, err := a.targetPath(dir)
	if err != nil {
		return err
	}
	if target == a.root {
		return nil
	}
	if err := a.checkExistingPrefix(filepath.Dir(target)); err != nil {
		return err
	}
	if err := os.MkdirAll(target, a.dirMode(visibility)); err != nil {
		return mapLocalError(err)
	}
	if err := a.checkExistingNoSymlink(target); err != nil {
		return err
	}
	if err := os.Chmod(target, a.dirMode(visibility)); err != nil {
		return mapLocalError(err)
	}
	return nil
}

func (a *Adapter) DeleteDirectory(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := a.existingPath(dir)
	if err != nil {
		return err
	}
	if target == a.root {
		return fmt.Errorf("%w: refusing to delete root", filesystem.ErrInvalidPath)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return mapLocalError(err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: not a directory", filesystem.ErrInvalidPath)
	}
	if err := os.RemoveAll(target); err != nil {
		return mapLocalError(err)
	}
	return nil
}

func (a *Adapter) DirectorySemantics() filesystem.DirectorySemantics {
	return filesystem.DirectoryReal
}

func (a *Adapter) URL(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !a.urlEnabled {
		return "", filesystem.ErrUnsupported
	}
	return a.baseURL + "/" + escapePath(path), nil
}

func (a *Adapter) TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.temporaryURLBuilder == nil {
		return "", filesystem.ErrUnsupported
	}
	return a.temporaryURLBuilder(ctx, path, expiresAt, opts)
}

func (a *Adapter) GetVisibility(ctx context.Context, path string) (filesystem.Visibility, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target, err := a.existingPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", mapLocalError(err)
	}
	mode := info.Mode().Perm()
	if info.IsDir() {
		if mode&0o005 != 0 {
			return filesystem.VisibilityPublic, nil
		}
		return filesystem.VisibilityPrivate, nil
	}
	if mode&0o004 != 0 {
		return filesystem.VisibilityPublic, nil
	}
	return filesystem.VisibilityPrivate, nil
}

func (a *Adapter) SetVisibility(ctx context.Context, path string, visibility filesystem.Visibility) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if visibility == "" || !visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	target, err := a.existingPath(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return mapLocalError(err)
	}
	mode := a.fileMode(visibility)
	if info.IsDir() {
		mode = a.dirMode(visibility)
	}
	if err := os.Chmod(target, mode); err != nil {
		return mapLocalError(err)
	}
	return nil
}

func (a *Adapter) MimeType(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	rc, err := a.Open(ctx, path)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var buf [512]byte
	n, err := io.ReadFull(rc, buf[:])
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", err
	}
	return http.DetectContentType(buf[:n]), nil
}

func (a *Adapter) atomicWrite(ctx context.Context, target string, r io.Reader, visibility filesystem.Visibility) error {
	temp, err := os.CreateTemp(filepath.Dir(target), ".tmp-"+filepath.Base(target)+"-*")
	if err != nil {
		return mapLocalError(err)
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if _, err := copyWithContext(ctx, temp, r); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(a.fileMode(visibility)); err != nil {
		_ = temp.Close()
		return mapLocalError(err)
	}
	if err := temp.Close(); err != nil {
		return mapLocalError(err)
	}
	if err := os.Rename(tempName, target); err != nil {
		return mapLocalError(err)
	}
	cleanup = false
	return nil
}

func (a *Adapter) directWrite(ctx context.Context, target string, r io.Reader, visibility filesystem.Visibility, overwrite bool) error {
	flags := os.O_WRONLY | os.O_CREATE
	if overwrite {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}
	file, err := os.OpenFile(target, flags, a.fileMode(visibility))
	if err != nil {
		return mapLocalError(err)
	}
	defer file.Close()
	if _, err := copyWithContext(ctx, file, r); err != nil {
		return err
	}
	if err := file.Chmod(a.fileMode(visibility)); err != nil {
		return mapLocalError(err)
	}
	return mapLocalError(file.Close())
}

func (a *Adapter) listRoot(prefix string) (string, string, error) {
	normalized, err := filesystem.NormalizePath(prefix)
	if err != nil {
		return "", "", err
	}
	target := a.root
	displayPrefix := ""
	if normalized != "" {
		target, err = a.existingPath(normalized)
		if err != nil {
			return "", "", err
		}
		info, err := os.Lstat(target)
		if err != nil {
			return "", "", mapLocalError(err)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("%w: list prefix is not a directory", filesystem.ErrInvalidPath)
		}
		displayPrefix = normalized
	}
	return target, displayPrefix, nil
}

func (a *Adapter) listFlat(ctx context.Context, dir string, displayPrefix string, cursor string, pageSize int) ([]filesystem.Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, mapLocalError(err)
	}
	items := make([]filesystem.Entry, 0, min(len(entries), pageSize+1))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: symlink in listed directory", filesystem.ErrInvalidPath)
		}
		entryPath := joinSlash(displayPrefix, entry.Name())
		if cursor != "" && entryPath <= cursor {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, mapLocalError(err)
		}
		items = append(items, toEntry(entryPath, info))
		if len(items) > pageSize {
			break
		}
	}
	return items, nil
}

func (a *Adapter) listRecursive(ctx context.Context, dir string, displayPrefix string, cursor string, pageSize int) ([]filesystem.Entry, error) {
	items := make([]filesystem.Entry, 0, pageSize+1)
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return mapLocalError(err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink in listed directory", filesystem.ErrInvalidPath)
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		entryPath := joinSlash(displayPrefix, filepath.ToSlash(rel))
		if cursor != "" && entryPath <= cursor {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return mapLocalError(err)
		}
		items = append(items, toEntry(entryPath, info))
		if len(items) > pageSize {
			return errStopWalk
		}
		return nil
	})
	if errors.Is(err, errStopWalk) {
		return items, nil
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})
	if len(items) > pageSize {
		items = items[:pageSize+1]
	}
	return items, nil
}

func (a *Adapter) targetPath(path string) (string, error) {
	normalized, err := filesystem.NormalizePath(path)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return a.root, nil
	}
	target := filepath.Join(a.root, filepath.FromSlash(normalized))
	rel, err := filepath.Rel(a.root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: path escapes root", filesystem.ErrInvalidPath)
	}
	return target, nil
}

func (a *Adapter) existingPath(path string) (string, error) {
	target, err := a.targetPath(path)
	if err != nil {
		return "", err
	}
	if err := a.checkExistingNoSymlink(target); err != nil {
		return "", err
	}
	return target, nil
}

func (a *Adapter) checkExistingNoSymlink(target string) error {
	return a.walkSegments(target, false)
}

func (a *Adapter) checkExistingPrefix(target string) error {
	return a.walkSegments(target, true)
}

func (a *Adapter) walkSegments(target string, allowMissing bool) error {
	rel, err := filepath.Rel(a.root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%w: path escapes root", filesystem.ErrInvalidPath)
	}
	current := a.root
	for _, segment := range strings.Split(rel, string(os.PathSeparator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				if allowMissing {
					return nil
				}
				return filesystem.ErrNotFound
			}
			return mapLocalError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink segment is not allowed", filesystem.ErrInvalidPath)
		}
	}
	return nil
}

func (a *Adapter) ensureTargetWritable(target string, overwrite bool) error {
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return mapLocalError(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: symlink target is not allowed", filesystem.ErrInvalidPath)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: target is a directory", filesystem.ErrAlreadyExists)
	}
	if !overwrite {
		return filesystem.ErrAlreadyExists
	}
	return nil
}

func (a *Adapter) fileMode(visibility filesystem.Visibility) fs.FileMode {
	if visibility == filesystem.VisibilityPublic {
		return a.permissions.FilePublic
	}
	return a.permissions.FilePrivate
}

func (a *Adapter) dirMode(visibility filesystem.Visibility) fs.FileMode {
	if visibility == filesystem.VisibilityPublic {
		return a.permissions.DirPublic
	}
	return a.permissions.DirPrivate
}

func copyWithContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 32*1024)
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		nr, er := src.Read(buf)
		if nr > 0 {
			if err := ctx.Err(); err != nil {
				return written, err
			}
			nw, ew := dst.Write(buf[:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
			if nr != nw {
				return written, io.ErrShortWrite
			}
		}
		if er != nil {
			if errors.Is(er, io.EOF) {
				return written, nil
			}
			return written, er
		}
	}
}

func mapLocalError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, filesystem.ErrInvalidPath) ||
		errors.Is(err, filesystem.ErrNotFound) ||
		errors.Is(err, filesystem.ErrAlreadyExists) ||
		errors.Is(err, filesystem.ErrUnsupported) ||
		errors.Is(err, filesystem.ErrInvalidVisibility) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		return filesystem.ErrNotFound
	}
	if errors.Is(err, os.ErrExist) {
		return filesystem.ErrAlreadyExists
	}
	return err
}

func encodeCursor(path string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(path))
}

func decodeCursor(cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", fmt.Errorf("%w: invalid cursor", filesystem.ErrInvalidPath)
	}
	return string(data), nil
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func joinSlash(prefix, name string) string {
	name = strings.Trim(name, "/")
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func toEntry(path string, info fs.FileInfo) filesystem.Entry {
	typ := filesystem.EntryFile
	if info.IsDir() {
		typ = filesystem.EntryDirectory
	}
	return filesystem.Entry{
		Path:         path,
		Type:         typ,
		Size:         info.Size(),
		LastModified: info.ModTime(),
	}
}
