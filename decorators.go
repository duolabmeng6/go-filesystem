package filesystem

import (
	"context"
	"io"
	"strings"
	"time"
)

func Scoped(adapter Adapter, prefix string) (Adapter, error) {
	if adapter == nil {
		return nil, ErrUnsupported
	}
	normalized, err := NormalizePath(prefix)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return adapter, nil
	}
	return &scopedAdapter{adapter: adapter, prefix: normalized}, nil
}

type scopedAdapter struct {
	adapter Adapter
	prefix  string
}

func (a *scopedAdapter) Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error {
	return a.adapter.Write(ctx, a.apply(path), r, opts)
}

func (a *scopedAdapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return a.adapter.Open(ctx, a.apply(path))
}

func (a *scopedAdapter) Delete(ctx context.Context, path string) error {
	return a.adapter.Delete(ctx, a.apply(path))
}

func (a *scopedAdapter) Exists(ctx context.Context, path string) (bool, error) {
	return a.adapter.Exists(ctx, a.apply(path))
}

func (a *scopedAdapter) Stat(ctx context.Context, path string) (FileInfo, error) {
	info, err := a.adapter.Stat(ctx, a.apply(path))
	info.Path = a.strip(info.Path)
	return info, err
}

func (a *scopedAdapter) ListPage(ctx context.Context, prefix string, opts ListOptions) (Page, error) {
	page, err := a.adapter.ListPage(ctx, a.apply(prefix), opts)
	if err != nil {
		return Page{}, err
	}
	for i := range page.Items {
		page.Items[i].Path = a.strip(page.Items[i].Path)
	}
	return page, nil
}

func (a *scopedAdapter) Capabilities() CapabilitySet {
	return a.adapter.Capabilities().Clone()
}

func (a *scopedAdapter) Copy(ctx context.Context, src string, dst string) error {
	copier, ok := a.adapter.(Copier)
	if !ok {
		return ErrUnsupported
	}
	return copier.Copy(ctx, a.apply(src), a.apply(dst))
}

func (a *scopedAdapter) Move(ctx context.Context, src string, dst string) error {
	mover, ok := a.adapter.(Mover)
	if !ok {
		return ErrUnsupported
	}
	return mover.Move(ctx, a.apply(src), a.apply(dst))
}

func (a *scopedAdapter) MakeDirectory(ctx context.Context, dir string, opts DirectoryOptions) error {
	manager, ok := a.adapter.(DirectoryManager)
	if !ok {
		return ErrUnsupported
	}
	return manager.MakeDirectory(ctx, a.apply(dir), opts)
}

func (a *scopedAdapter) DeleteDirectory(ctx context.Context, dir string) error {
	manager, ok := a.adapter.(DirectoryManager)
	if !ok {
		return ErrUnsupported
	}
	return manager.DeleteDirectory(ctx, a.apply(dir))
}

func (a *scopedAdapter) DirectorySemantics() DirectorySemantics {
	provider, ok := a.adapter.(DirectorySemanticsProvider)
	if !ok {
		return DirectoryUnsupported
	}
	return provider.DirectorySemantics()
}

func (a *scopedAdapter) URL(ctx context.Context, path string) (string, error) {
	generator, ok := a.adapter.(URLGenerator)
	if !ok {
		return "", ErrUnsupported
	}
	return generator.URL(ctx, a.apply(path))
}

func (a *scopedAdapter) TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts URLOptions) (string, error) {
	generator, ok := a.adapter.(TemporaryURLGenerator)
	if !ok {
		return "", ErrUnsupported
	}
	return generator.TemporaryURL(ctx, a.apply(path), expiresAt, opts)
}

func (a *scopedAdapter) GetVisibility(ctx context.Context, path string) (Visibility, error) {
	controller, ok := a.adapter.(VisibilityController)
	if !ok {
		return "", ErrUnsupported
	}
	return controller.GetVisibility(ctx, a.apply(path))
}

func (a *scopedAdapter) SetVisibility(ctx context.Context, path string, visibility Visibility) error {
	controller, ok := a.adapter.(VisibilityController)
	if !ok {
		return ErrUnsupported
	}
	return controller.SetVisibility(ctx, a.apply(path), visibility)
}

func (a *scopedAdapter) apply(path string) string {
	if path == "" {
		return a.prefix
	}
	return a.prefix + "/" + path
}

func (a *scopedAdapter) strip(path string) string {
	if path == a.prefix {
		return ""
	}
	return strings.TrimPrefix(path, a.prefix+"/")
}

func ReadOnly(adapter Adapter) Adapter {
	return &readOnlyAdapter{adapter: adapter}
}

type readOnlyAdapter struct {
	adapter Adapter
}

func (a *readOnlyAdapter) Write(context.Context, string, io.Reader, WriteOptions) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	return a.adapter.Open(ctx, path)
}

func (a *readOnlyAdapter) Delete(context.Context, string) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) Exists(ctx context.Context, path string) (bool, error) {
	return a.adapter.Exists(ctx, path)
}

func (a *readOnlyAdapter) Stat(ctx context.Context, path string) (FileInfo, error) {
	return a.adapter.Stat(ctx, path)
}

func (a *readOnlyAdapter) ListPage(ctx context.Context, prefix string, opts ListOptions) (Page, error) {
	return a.adapter.ListPage(ctx, prefix, opts)
}

func (a *readOnlyAdapter) Capabilities() CapabilitySet {
	return a.adapter.Capabilities().Without(CapabilityCopy, CapabilityMove, CapabilityDirectory)
}

func (a *readOnlyAdapter) Copy(context.Context, string, string) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) Move(context.Context, string, string) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) MakeDirectory(context.Context, string, DirectoryOptions) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) DeleteDirectory(context.Context, string) error {
	return ErrReadOnly
}

func (a *readOnlyAdapter) DirectorySemantics() DirectorySemantics {
	provider, ok := a.adapter.(DirectorySemanticsProvider)
	if !ok {
		return DirectoryUnsupported
	}
	return provider.DirectorySemantics()
}

func (a *readOnlyAdapter) URL(ctx context.Context, path string) (string, error) {
	generator, ok := a.adapter.(URLGenerator)
	if !ok {
		return "", ErrUnsupported
	}
	return generator.URL(ctx, path)
}

func (a *readOnlyAdapter) TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts URLOptions) (string, error) {
	generator, ok := a.adapter.(TemporaryURLGenerator)
	if !ok {
		return "", ErrUnsupported
	}
	return generator.TemporaryURL(ctx, path, expiresAt, opts)
}

func (a *readOnlyAdapter) GetVisibility(ctx context.Context, path string) (Visibility, error) {
	controller, ok := a.adapter.(VisibilityController)
	if !ok {
		return "", ErrUnsupported
	}
	return controller.GetVisibility(ctx, path)
}

func (a *readOnlyAdapter) SetVisibility(context.Context, string, Visibility) error {
	return ErrReadOnly
}

func isReadOnlyAdapter(adapter Adapter) bool {
	_, ok := adapter.(*readOnlyAdapter)
	if ok {
		return true
	}
	type unwrapper interface {
		Unwrap() Adapter
	}
	if wrapped, ok := adapter.(unwrapper); ok {
		return isReadOnlyAdapter(wrapped.Unwrap())
	}
	return false
}

func (a *scopedAdapter) Unwrap() Adapter {
	return a.adapter
}

func (a *readOnlyAdapter) Unwrap() Adapter {
	return a.adapter
}
