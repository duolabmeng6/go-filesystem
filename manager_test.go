package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"
)

type memoryAdapter struct {
	files map[string][]byte
}

func newMemoryAdapter() *memoryAdapter {
	return &memoryAdapter{files: map[string][]byte{}}
}

func (a *memoryAdapter) Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.Overwrite {
		if _, ok := a.files[path]; ok {
			return ErrAlreadyExists
		}
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	a.files[path] = data
	return nil
}

func (a *memoryAdapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, ok := a.files[path]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (a *memoryAdapter) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := a.files[path]; !ok {
		return ErrNotFound
	}
	delete(a.files, path)
	return nil
}

func (a *memoryAdapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, ok := a.files[path]
	return ok, nil
}

func (a *memoryAdapter) Stat(ctx context.Context, path string) (FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	data, ok := a.files[path]
	if !ok {
		return FileInfo{}, ErrNotFound
	}
	return FileInfo{Path: path, Size: int64(len(data))}, nil
}

func (a *memoryAdapter) ListPage(context.Context, string, ListOptions) (Page, error) {
	return Page{}, nil
}

func (a *memoryAdapter) Capabilities() CapabilitySet {
	return NewCapabilitySet()
}

func TestManagerBuildsDiskWithoutHoldingLock(t *testing.T) {
	m := New()
	factoryStarted := make(chan struct{})
	factoryCanReturn := make(chan struct{})
	if err := m.Extend("memory", func(ctx context.Context, config DiskConfig) (Adapter, error) {
		close(factoryStarted)
		if err := m.SetDefaultDisk("other"); err != nil {
			t.Errorf("factory should not run under manager lock: %v", err)
		}
		<-factoryCanReturn
		return newMemoryAdapter(), nil
	}); err != nil {
		t.Fatalf("extend: %v", err)
	}
	if err := m.RegisterDisk("other", NewDisk(newMemoryAdapter())); err != nil {
		t.Fatalf("register other: %v", err)
	}
	m.configs["mem"] = DiskConfig{Driver: "memory"}
	errCh := make(chan error, 1)
	go func() {
		_, err := m.Disk("mem")
		errCh <- err
	}()
	<-factoryStarted
	close(factoryCanReturn)
	if err := <-errCh; err != nil {
		t.Fatalf("disk: %v", err)
	}
}

func TestReplaceDiskKeepsOldHolder(t *testing.T) {
	m := New(WithDefaultDisk("mem"))
	oldDisk := NewDisk(newMemoryAdapter())
	if err := m.RegisterDisk("mem", oldDisk); err != nil {
		t.Fatalf("register: %v", err)
	}
	held, err := m.Disk("mem")
	if err != nil {
		t.Fatalf("disk: %v", err)
	}
	if err := held.Put(context.Background(), "old.txt", []byte("old")); err != nil {
		t.Fatalf("put old: %v", err)
	}
	newDisk := NewDisk(newMemoryAdapter())
	if err := m.ReplaceDisk("mem", newDisk); err != nil {
		t.Fatalf("replace: %v", err)
	}
	current, err := m.Disk("mem")
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	exists, err := current.Exists(context.Background(), "old.txt")
	if err != nil {
		t.Fatalf("exists current: %v", err)
	}
	if exists {
		t.Fatalf("replacement unexpectedly sees old holder data")
	}
	exists, err = held.Exists(context.Background(), "old.txt")
	if err != nil || !exists {
		t.Fatalf("old holder exists=%v err=%v", exists, err)
	}
}

func TestDeleteManyMultiError(t *testing.T) {
	disk := NewDisk(newMemoryAdapter())
	if err := disk.Put(context.Background(), "a.txt", []byte("a")); err != nil {
		t.Fatalf("put: %v", err)
	}
	err := disk.DeleteMany(context.Background(), []string{"a.txt", "missing.txt"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found in multi error, got %v", err)
	}
	if err := disk.DeleteMany(context.Background(), []string{"missing.txt"}, WithIgnoreMissing()); err != nil {
		t.Fatalf("ignore missing: %v", err)
	}
}

func TestPrefixOnlyDirectoryStatAndMove(t *testing.T) {
	adapter := newPrefixOnlyMemoryAdapter()
	disk := NewDisk(adapter)
	ctx := context.Background()
	for _, item := range []struct {
		path string
		data string
	}{
		{"photos/2026/a.jpg", "a"},
		{"photos/2026/nested/b.jpg", "b"},
		{"photos/keep.txt", "keep"},
	} {
		if err := disk.Put(ctx, item.path, []byte(item.data)); err != nil {
			t.Fatalf("put %s: %v", item.path, err)
		}
	}
	info, err := disk.Stat(ctx, "photos/2026")
	if err != nil {
		t.Fatalf("stat prefix directory: %v", err)
	}
	if !info.IsDir || info.Path != "photos/2026" {
		t.Fatalf("unexpected stat=%+v", info)
	}
	exists, err := disk.Exists(ctx, "photos/2026")
	if err != nil || !exists {
		t.Fatalf("exists prefix directory=%v err=%v", exists, err)
	}
	if err := disk.Move(ctx, "photos/2026", "archive/2026"); err != nil {
		t.Fatalf("move prefix directory: %v", err)
	}
	for _, path := range []string{"archive/2026/a.jpg", "archive/2026/nested/b.jpg", "photos/keep.txt"} {
		exists, err := disk.Exists(ctx, path)
		if err != nil || !exists {
			t.Fatalf("expected %s to exist, exists=%v err=%v", path, exists, err)
		}
	}
	for _, path := range []string{"photos/2026/a.jpg", "photos/2026/nested/b.jpg"} {
		exists, err := disk.Exists(ctx, path)
		if err != nil {
			t.Fatalf("exists %s: %v", path, err)
		}
		if exists {
			t.Fatalf("expected %s to be moved away", path)
		}
	}
	if err := disk.Move(ctx, "archive", "archive/inside"); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("expected invalid recursive move, got %v", err)
	}
}

func TestPrefixOnlyDirectoryCopyAndDelete(t *testing.T) {
	adapter := newPrefixOnlyMemoryAdapter()
	disk := NewDisk(adapter)
	ctx := context.Background()
	if err := disk.Put(ctx, "dir/a.txt", []byte("a")); err != nil {
		t.Fatalf("put a: %v", err)
	}
	if err := disk.Put(ctx, "dir/nested/b.txt", []byte("b")); err != nil {
		t.Fatalf("put b: %v", err)
	}
	if err := disk.Copy(ctx, "dir", "copy"); err != nil {
		t.Fatalf("copy prefix directory: %v", err)
	}
	for _, path := range []string{"dir/a.txt", "dir/nested/b.txt", "copy/a.txt", "copy/nested/b.txt"} {
		exists, err := disk.Exists(ctx, path)
		if err != nil || !exists {
			t.Fatalf("expected %s to exist, exists=%v err=%v", path, exists, err)
		}
	}
	if err := disk.DeleteDirectory(ctx, "dir"); err != nil {
		t.Fatalf("delete prefix directory: %v", err)
	}
	for _, path := range []string{"dir/a.txt", "dir/nested/b.txt"} {
		exists, err := disk.Exists(ctx, path)
		if err != nil {
			t.Fatalf("exists %s: %v", path, err)
		}
		if exists {
			t.Fatalf("expected %s to be deleted", path)
		}
	}
}

type prefixOnlyMemoryAdapter struct {
	*memoryAdapter
	modified map[string]time.Time
}

func newPrefixOnlyMemoryAdapter() *prefixOnlyMemoryAdapter {
	return &prefixOnlyMemoryAdapter{
		memoryAdapter: newMemoryAdapter(),
		modified:      map[string]time.Time{},
	}
}

func (a *prefixOnlyMemoryAdapter) Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error {
	if err := a.memoryAdapter.Write(ctx, path, r, opts); err != nil {
		return err
	}
	a.modified[path] = time.Now().UTC()
	return nil
}

func (a *prefixOnlyMemoryAdapter) Delete(ctx context.Context, path string) error {
	if err := a.memoryAdapter.Delete(ctx, path); err != nil {
		return err
	}
	delete(a.modified, path)
	return nil
}

func (a *prefixOnlyMemoryAdapter) Stat(ctx context.Context, path string) (FileInfo, error) {
	info, err := a.memoryAdapter.Stat(ctx, path)
	if err != nil {
		return FileInfo{}, err
	}
	info.LastModified = a.modified[path]
	return info, nil
}

func (a *prefixOnlyMemoryAdapter) ListPage(ctx context.Context, prefix string, opts ListOptions) (Page, error) {
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	listPrefix := prefix
	if listPrefix != "" {
		listPrefix += "/"
	}
	keys := make([]string, 0, len(a.files))
	for key := range a.files {
		if strings.HasPrefix(key, listPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if opts.Cursor != "" {
		for i, key := range keys {
			if key > opts.Cursor {
				start = i
				break
			}
			start = len(keys)
		}
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 1000
	}
	page := Page{}
	seenDirs := map[string]struct{}{}
	for _, key := range keys[start:] {
		if len(page.Items) >= pageSize {
			page.NextCursor = key
			break
		}
		if !opts.Recursive {
			rest := strings.TrimPrefix(key, listPrefix)
			if idx := strings.Index(rest, "/"); idx >= 0 {
				dir := strings.TrimSuffix(listPrefix+rest[:idx+1], "/")
				if _, ok := seenDirs[dir]; ok {
					continue
				}
				seenDirs[dir] = struct{}{}
				page.Items = append(page.Items, Entry{Path: dir, Type: EntryDirectory})
				continue
			}
		}
		page.Items = append(page.Items, Entry{Path: key, Type: EntryFile, Size: int64(len(a.files[key])), LastModified: a.modified[key]})
	}
	return page, nil
}

func (a *prefixOnlyMemoryAdapter) DirectorySemantics() DirectorySemantics {
	return DirectoryPrefixOnly
}

func (a *prefixOnlyMemoryAdapter) Capabilities() CapabilitySet {
	return NewCapabilitySet(CapabilityCopy)
}

func (a *prefixOnlyMemoryAdapter) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, ok := a.files[src]
	if !ok {
		return ErrNotFound
	}
	cloned := append([]byte(nil), data...)
	a.files[dst] = cloned
	a.modified[dst] = a.modified[src]
	return nil
}
