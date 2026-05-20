package filesystem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Disk struct {
	name    string
	adapter Adapter
}

type DiskOption func(*Disk)

func WithDiskName(name string) DiskOption {
	return func(d *Disk) {
		d.name = name
	}
}

func NewDisk(adapter Adapter, opts ...DiskOption) *Disk {
	d := &Disk{adapter: adapter}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

func (d *Disk) Name() string {
	return d.name
}

func (d *Disk) Adapter() Adapter {
	return d.adapter
}

func (d *Disk) withName(name string) *Disk {
	if d.name == name {
		return d
	}
	clone := *d
	clone.name = name
	return &clone
}

func (d *Disk) Put(ctx context.Context, path string, data []byte, opts ...WriteOption) error {
	return d.Write(ctx, path, bytes.NewReader(data), opts...)
}

func (d *Disk) Write(ctx context.Context, p string, r io.Reader, opts ...WriteOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return newOpError("write", d.name, p, err)
	}
	options, err := applyWriteOptions(opts)
	if err != nil {
		return newOpError("write", d.name, normalized, err)
	}
	if r == nil {
		return newOpError("write", d.name, normalized, fmt.Errorf("%w: nil reader", ErrInvalidPath))
	}
	return newOpError("write", d.name, normalized, d.adapter.Write(ctx, normalized, r, options))
}

func (d *Disk) Get(ctx context.Context, p string) ([]byte, error) {
	rc, err := d.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		normalized, _ := NormalizePath(p)
		return nil, newOpError("get", d.name, normalized, err)
	}
	return data, nil
}

func (d *Disk) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return nil, newOpError("open", d.name, p, err)
	}
	rc, err := d.adapter.Open(ctx, normalized)
	return rc, newOpError("open", d.name, normalized, err)
}

func (d *Disk) Delete(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return newOpError("delete", d.name, p, err)
	}
	return newOpError("delete", d.name, normalized, d.adapter.Delete(ctx, normalized))
}

func (d *Disk) DeleteIfExists(ctx context.Context, p string) error {
	err := d.Delete(ctx, p)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (d *Disk) DeleteMany(ctx context.Context, paths []string, opts ...DeleteOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	options := applyDeleteOptions(opts)
	var multi MultiError
	multi.Op = "delete many"
	for _, p := range paths {
		err := d.Delete(ctx, p)
		if err == nil {
			continue
		}
		if options.IgnoreMissing && errors.Is(err, ErrNotFound) {
			continue
		}
		multi.Errors = append(multi.Errors, PathError{Path: p, Err: err})
	}
	if len(multi.Errors) == 0 {
		return nil
	}
	return &multi
}

func (d *Disk) Exists(ctx context.Context, p string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return false, newOpError("exists", d.name, p, err)
	}
	exists, err := d.adapter.Exists(ctx, normalized)
	if errors.Is(err, ErrNotFound) {
		return d.hasPrefixOnlyDirectory(ctx, normalized), nil
	}
	if err == nil && !exists && d.hasPrefixOnlyDirectory(ctx, normalized) {
		return true, nil
	}
	return exists, newOpError("exists", d.name, normalized, err)
}

func (d *Disk) Missing(ctx context.Context, p string) (bool, error) {
	exists, err := d.Exists(ctx, p)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func (d *Disk) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := normalizeNonEmptyPath(src)
	if err != nil {
		return newOpError("copy", d.name, src, err)
	}
	destination, err := normalizeNonEmptyPath(dst)
	if err != nil {
		return newOpError("copy", d.name, dst, err)
	}
	if isReadOnlyAdapter(d.adapter) {
		return newOpError("copy", d.name, destination, ErrReadOnly)
	}
	if d.isPrefixOnlyDirectory(ctx, source) {
		return newOpError("copy", d.name, destination, d.copyPrefixDirectory(ctx, source, destination))
	}
	if d.adapter.Capabilities().Has(CapabilityCopy) {
		copier, ok := d.adapter.(Copier)
		if !ok {
			return newOpError("copy", d.name, destination, ErrUnsupported)
		}
		return newOpError("copy", d.name, destination, copier.Copy(ctx, source, destination))
	}
	rc, err := d.adapter.Open(ctx, source)
	if err != nil {
		return newOpError("copy", d.name, source, err)
	}
	defer rc.Close()
	return newOpError("copy", d.name, destination, d.adapter.Write(ctx, destination, rc, DefaultWriteOptions()))
}

func (d *Disk) Move(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := normalizeNonEmptyPath(src)
	if err != nil {
		return newOpError("move", d.name, src, err)
	}
	destination, err := normalizeNonEmptyPath(dst)
	if err != nil {
		return newOpError("move", d.name, dst, err)
	}
	if isReadOnlyAdapter(d.adapter) {
		return newOpError("move", d.name, destination, ErrReadOnly)
	}
	if d.isPrefixOnlyDirectory(ctx, source) {
		return newOpError("move", d.name, destination, d.movePrefixDirectory(ctx, source, destination))
	}
	if d.adapter.Capabilities().Has(CapabilityMove) {
		mover, ok := d.adapter.(Mover)
		if !ok {
			return newOpError("move", d.name, destination, ErrUnsupported)
		}
		return newOpError("move", d.name, destination, mover.Move(ctx, source, destination))
	}
	if err := d.Copy(ctx, source, destination); err != nil {
		return err
	}
	if err := d.adapter.Delete(ctx, source); err != nil {
		return &OpError{
			Op:      "move",
			Disk:    d.name,
			Path:    source,
			Partial: true,
			Err:     fmt.Errorf("%w: copy succeeded but deleting source failed; source and destination may both exist: %v", ErrPartialFailure, err),
		}
	}
	return nil
}

func (d *Disk) Stat(ctx context.Context, p string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return FileInfo{}, newOpError("stat", d.name, p, err)
	}
	info, err := d.adapter.Stat(ctx, normalized)
	if errors.Is(err, ErrNotFound) && d.hasPrefixOnlyDirectory(ctx, normalized) {
		return FileInfo{Path: normalized, IsDir: true}, nil
	}
	return info, newOpError("stat", d.name, normalized, err)
}

func (d *Disk) Size(ctx context.Context, p string) (int64, error) {
	info, err := d.Stat(ctx, p)
	if err != nil {
		return 0, err
	}
	return info.Size, nil
}

func (d *Disk) hasPrefixOnlyDirectory(ctx context.Context, p string) bool {
	provider, ok := d.adapter.(DirectorySemanticsProvider)
	if !ok || provider.DirectorySemantics() != DirectoryPrefixOnly {
		return false
	}
	page, err := d.adapter.ListPage(ctx, p, ListOptions{PageSize: 1})
	return err == nil && len(page.Items) > 0
}

func (d *Disk) isPrefixOnlyDirectory(ctx context.Context, p string) bool {
	provider, ok := d.adapter.(DirectorySemanticsProvider)
	if !ok || provider.DirectorySemantics() != DirectoryPrefixOnly {
		return false
	}
	if _, err := d.adapter.Stat(ctx, p); err == nil {
		return false
	} else if !errors.Is(err, ErrNotFound) {
		return false
	}
	page, err := d.adapter.ListPage(ctx, p, ListOptions{PageSize: 1})
	return err == nil && len(page.Items) > 0
}

func (d *Disk) copyPrefixDirectory(ctx context.Context, source string, destination string) error {
	if source == destination {
		return nil
	}
	if isSameOrChildPath(destination, source) {
		return fmt.Errorf("%w: cannot copy a prefix directory into itself", ErrInvalidPath)
	}
	it, err := d.List(ctx, source, WithRecursive(true))
	if err != nil {
		return err
	}
	defer it.Close()
	copied := 0
	for {
		entry, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			if copied == 0 {
				return ErrNotFound
			}
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type != EntryFile {
			continue
		}
		target := replacePathPrefix(entry.Path, source, destination)
		if err := d.Copy(ctx, entry.Path, target); err != nil {
			return err
		}
		copied++
	}
}

func (d *Disk) movePrefixDirectory(ctx context.Context, source string, destination string) error {
	if source == destination {
		return nil
	}
	if isSameOrChildPath(destination, source) {
		return fmt.Errorf("%w: cannot move a prefix directory into itself", ErrInvalidPath)
	}
	it, err := d.List(ctx, source, WithRecursive(true))
	if err != nil {
		return err
	}
	defer it.Close()
	var moved []string
	for {
		entry, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			if len(moved) == 0 {
				return ErrNotFound
			}
			if err := d.cleanupMovedPrefixDirectory(ctx, moved); err != nil {
				return &OpError{
					Op:      "move",
					Disk:    d.name,
					Path:    source,
					Partial: true,
					Err:     fmt.Errorf("%w: copy succeeded but deleting source objects failed; source and destination may both exist: %v", ErrPartialFailure, err),
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type != EntryFile {
			continue
		}
		target := replacePathPrefix(entry.Path, source, destination)
		if err := d.Copy(ctx, entry.Path, target); err != nil {
			return err
		}
		moved = append(moved, entry.Path)
	}
}

func (d *Disk) cleanupMovedPrefixDirectory(ctx context.Context, paths []string) error {
	var multi MultiError
	multi.Op = "move cleanup"
	for _, p := range paths {
		if err := d.adapter.Delete(ctx, p); err != nil {
			multi.Errors = append(multi.Errors, PathError{Path: p, Err: err})
		}
	}
	if len(multi.Errors) == 0 {
		return nil
	}
	return &multi
}

func replacePathPrefix(p string, source string, destination string) string {
	if p == source {
		return destination
	}
	return destination + strings.TrimPrefix(p, source)
}

func isSameOrChildPath(path string, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+"/")
}

func (d *Disk) LastModified(ctx context.Context, p string) (time.Time, error) {
	info, err := d.Stat(ctx, p)
	if err != nil {
		return time.Time{}, err
	}
	return info.LastModified, nil
}

func (d *Disk) MimeType(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return "", newOpError("mime type", d.name, p, err)
	}
	if detector, ok := d.adapter.(interface {
		MimeType(context.Context, string) (string, error)
	}); ok {
		mime, err := detector.MimeType(ctx, normalized)
		return mime, newOpError("mime type", d.name, normalized, err)
	}
	rc, err := d.adapter.Open(ctx, normalized)
	if err != nil {
		return "", newOpError("mime type", d.name, normalized, err)
	}
	defer rc.Close()
	var buf [512]byte
	n, readErr := io.ReadFull(rc, buf[:])
	if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
		return "", newOpError("mime type", d.name, normalized, readErr)
	}
	return http.DetectContentType(buf[:n]), nil
}

func (d *Disk) Files(ctx context.Context, dir string) ([]string, error) {
	it, err := d.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	return collectIterator(ctx, it, EntryFile)
}

func (d *Disk) AllFiles(ctx context.Context, dir string) ([]string, error) {
	it, err := d.List(ctx, dir, WithRecursive(true))
	if err != nil {
		return nil, err
	}
	return collectIterator(ctx, it, EntryFile)
}

func (d *Disk) Directories(ctx context.Context, dir string) ([]string, error) {
	it, err := d.List(ctx, dir)
	if err != nil {
		return nil, err
	}
	return collectIterator(ctx, it, EntryDirectory)
}

func (d *Disk) AllDirectories(ctx context.Context, dir string) ([]string, error) {
	it, err := d.List(ctx, dir, WithRecursive(true))
	if err != nil {
		return nil, err
	}
	return collectIterator(ctx, it, EntryDirectory)
}

func (d *Disk) MakeDirectory(ctx context.Context, dir string, opts ...DirectoryOption) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeNonEmptyPath(dir)
	if err != nil {
		return newOpError("make directory", d.name, dir, err)
	}
	options, err := applyDirectoryOptions(opts)
	if err != nil {
		return newOpError("make directory", d.name, normalized, err)
	}
	if isReadOnlyAdapter(d.adapter) {
		return newOpError("make directory", d.name, normalized, ErrReadOnly)
	}
	if d.adapter.Capabilities().Has(CapabilityDirectory) {
		manager, ok := d.adapter.(DirectoryManager)
		if !ok {
			return newOpError("make directory", d.name, normalized, ErrUnsupported)
		}
		return newOpError("make directory", d.name, normalized, manager.MakeDirectory(ctx, normalized, options))
	}
	if provider, ok := d.adapter.(DirectorySemanticsProvider); ok && provider.DirectorySemantics() == DirectoryPrefixOnly {
		return nil
	}
	return newOpError("make directory", d.name, normalized, ErrUnsupported)
}

func (d *Disk) DeleteDirectory(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeNonEmptyPath(dir)
	if err != nil {
		return newOpError("delete directory", d.name, dir, err)
	}
	if isReadOnlyAdapter(d.adapter) {
		return newOpError("delete directory", d.name, normalized, ErrReadOnly)
	}
	if d.adapter.Capabilities().Has(CapabilityDirectory) {
		manager, ok := d.adapter.(DirectoryManager)
		if !ok {
			return newOpError("delete directory", d.name, normalized, ErrUnsupported)
		}
		return newOpError("delete directory", d.name, normalized, manager.DeleteDirectory(ctx, normalized))
	}
	provider, ok := d.adapter.(DirectorySemanticsProvider)
	if !ok || provider.DirectorySemantics() != DirectoryPrefixOnly {
		return newOpError("delete directory", d.name, normalized, ErrUnsupported)
	}
	it, err := d.List(ctx, normalized, WithRecursive(true))
	if err != nil {
		return err
	}
	defer it.Close()
	for {
		entry, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.Type == EntryFile {
			if err := d.Delete(ctx, entry.Path); err != nil {
				return err
			}
		}
	}
}

func (d *Disk) List(ctx context.Context, prefix string, opts ...ListOption) (EntryIterator, error) {
	options := applyListOptions(opts)
	normalized, err := NormalizePath(prefix)
	if err != nil {
		return nil, newOpError("list", d.name, prefix, err)
	}
	return &pageIterator{disk: d, prefix: normalized, opts: options}, nil
}

func (d *Disk) ListPage(ctx context.Context, prefix string, opts ...ListOption) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	normalized, err := NormalizePath(prefix)
	if err != nil {
		return Page{}, newOpError("list page", d.name, prefix, err)
	}
	options := applyListOptions(opts)
	page, err := d.adapter.ListPage(ctx, normalized, options)
	return page, newOpError("list page", d.name, normalized, err)
}

func (d *Disk) URL(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return "", newOpError("url", d.name, p, err)
	}
	if !d.adapter.Capabilities().Has(CapabilityURL) {
		return "", newOpError("url", d.name, normalized, ErrUnsupported)
	}
	generator, ok := d.adapter.(URLGenerator)
	if !ok {
		return "", newOpError("url", d.name, normalized, ErrUnsupported)
	}
	url, err := generator.URL(ctx, normalized)
	return url, newOpError("url", d.name, normalized, err)
}

func (d *Disk) TemporaryURL(ctx context.Context, p string, expiresAt time.Time, opts ...URLOption) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return "", newOpError("temporary url", d.name, p, err)
	}
	if !expiresAt.After(time.Now()) {
		return "", newOpError("temporary url", d.name, normalized, ErrInvalidExpiration)
	}
	if !d.adapter.Capabilities().Has(CapabilityTemporaryURL) {
		return "", newOpError("temporary url", d.name, normalized, ErrUnsupported)
	}
	generator, ok := d.adapter.(TemporaryURLGenerator)
	if !ok {
		return "", newOpError("temporary url", d.name, normalized, ErrUnsupported)
	}
	url, err := generator.TemporaryURL(ctx, normalized, expiresAt, applyURLOptions(opts))
	return url, newOpError("temporary url", d.name, normalized, err)
}

func (d *Disk) GetVisibility(ctx context.Context, p string) (Visibility, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return "", newOpError("get visibility", d.name, p, err)
	}
	if !d.adapter.Capabilities().Has(CapabilityVisibility) {
		return "", newOpError("get visibility", d.name, normalized, ErrUnsupported)
	}
	controller, ok := d.adapter.(VisibilityController)
	if !ok {
		return "", newOpError("get visibility", d.name, normalized, ErrUnsupported)
	}
	visibility, err := controller.GetVisibility(ctx, normalized)
	return visibility, newOpError("get visibility", d.name, normalized, err)
}

func (d *Disk) SetVisibility(ctx context.Context, p string, visibility Visibility) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := normalizeNonEmptyPath(p)
	if err != nil {
		return newOpError("set visibility", d.name, p, err)
	}
	if !visibility.Valid() || visibility == "" {
		return newOpError("set visibility", d.name, normalized, ErrInvalidVisibility)
	}
	if isReadOnlyAdapter(d.adapter) {
		return newOpError("set visibility", d.name, normalized, ErrReadOnly)
	}
	if !d.adapter.Capabilities().Has(CapabilityVisibility) {
		return newOpError("set visibility", d.name, normalized, ErrUnsupported)
	}
	controller, ok := d.adapter.(VisibilityController)
	if !ok {
		return newOpError("set visibility", d.name, normalized, ErrUnsupported)
	}
	return newOpError("set visibility", d.name, normalized, controller.SetVisibility(ctx, normalized, visibility))
}
