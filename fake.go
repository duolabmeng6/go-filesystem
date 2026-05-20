package filesystem

import (
	"context"
	"testing"
)

type FakeDisk struct {
	*Disk
	t       testing.TB
	manager *Manager
	name    string
	root    string
}

type FakeOptions struct {
	Visibility Visibility
}

type FakeOption func(*FakeOptions)

func WithFakeVisibility(visibility Visibility) FakeOption {
	return func(o *FakeOptions) {
		o.Visibility = visibility
	}
}

type fakeLocalConfig struct {
	Root       string
	Visibility Visibility
}

type FakeLocalConfig = fakeLocalConfig

type fakeLocalDiskFactory func(FakeLocalConfig) (*Disk, error)

var fakeDiskFactory fakeLocalDiskFactory

func RegisterFakeLocalFactory(factory fakeLocalDiskFactory) {
	fakeDiskFactory = factory
}

func Fake(t testing.TB, manager *Manager, name string, opts ...FakeOption) *FakeDisk {
	t.Helper()
	if manager == nil {
		t.Fatalf("filesystem fake: manager is nil")
	}
	root := t.TempDir()
	fake, err := newFakeDisk(t, root, opts...)
	if err != nil {
		t.Fatalf("filesystem fake: %v", err)
	}
	original, hadOriginal := manager.snapshotDisk(name)
	if err := manager.ReplaceDisk(name, fake.Disk); err != nil {
		t.Fatalf("filesystem fake: %v", err)
	}
	fake.manager = manager
	fake.name = name
	t.Cleanup(func() {
		if hadOriginal {
			_ = manager.ReplaceDisk(name, original)
			return
		}
		manager.removeDisk(name)
	})
	return fake
}

func PersistentFake(manager *Manager, name string, root string, opts ...FakeOption) (*FakeDisk, error) {
	if manager == nil {
		return nil, ErrDiskNotFound
	}
	fake, err := newFakeDisk(nil, root, opts...)
	if err != nil {
		return nil, err
	}
	if err := manager.ReplaceDisk(name, fake.Disk); err != nil {
		return nil, err
	}
	fake.manager = manager
	fake.name = name
	return fake, nil
}

func NewFakeDisk(t testing.TB, opts ...FakeOption) *FakeDisk {
	t.Helper()
	fake, err := newFakeDisk(t, t.TempDir(), opts...)
	if err != nil {
		t.Fatalf("filesystem fake: %v", err)
	}
	return fake
}

func newFakeDisk(t testing.TB, root string, opts ...FakeOption) (*FakeDisk, error) {
	options := FakeOptions{Visibility: VisibilityPrivate}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if !options.Visibility.Valid() {
		return nil, ErrInvalidVisibility
	}
	if fakeDiskFactory == nil {
		return nil, ErrUnsupported
	}
	disk, err := fakeDiskFactory(fakeLocalConfig{
		Root:       root,
		Visibility: options.Visibility,
	})
	if err != nil {
		return nil, err
	}
	return &FakeDisk{Disk: disk, t: t, root: root}, nil
}

func (f *FakeDisk) Root() string {
	return f.root
}

func (f *FakeDisk) AssertExists(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		exists, err := f.Exists(context.Background(), path)
		if err != nil {
			f.t.Fatalf("expected %q to exist, got error: %v", path, err)
		}
		if !exists {
			f.t.Fatalf("expected %q to exist", path)
		}
	}
}

func (f *FakeDisk) AssertMissing(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		exists, err := f.Exists(context.Background(), path)
		if err != nil {
			f.t.Fatalf("expected %q to be missing, got error: %v", path, err)
		}
		if exists {
			f.t.Fatalf("expected %q to be missing", path)
		}
	}
}

func (f *FakeDisk) AssertCount(dir string, expected int) {
	f.t.Helper()
	files, err := f.AllFiles(context.Background(), dir)
	if err != nil {
		f.t.Fatalf("count files in %q: %v", dir, err)
	}
	if len(files) != expected {
		f.t.Fatalf("expected %d file(s) in %q, got %d", expected, dir, len(files))
	}
}

func (f *FakeDisk) AssertDirectoryEmpty(dir string) {
	f.t.Helper()
	files, err := f.AllFiles(context.Background(), dir)
	if err != nil {
		f.t.Fatalf("list files in %q: %v", dir, err)
	}
	dirs, err := f.AllDirectories(context.Background(), dir)
	if err != nil {
		f.t.Fatalf("list directories in %q: %v", dir, err)
	}
	if len(files) != 0 || len(dirs) != 0 {
		f.t.Fatalf("expected directory %q to be empty, got %d file(s) and %d directorie(s)", dir, len(files), len(dirs))
	}
}

func (f *FakeDisk) AssertContent(path string, expected []byte) {
	f.t.Helper()
	actual, err := f.Get(context.Background(), path)
	if err != nil {
		f.t.Fatalf("read %q: %v", path, err)
	}
	if string(actual) != string(expected) {
		f.t.Fatalf("content mismatch for %q: got %q, want %q", path, actual, expected)
	}
}
