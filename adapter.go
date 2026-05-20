package filesystem

import (
	"context"
	"io"
	"time"
)

type Capability string

const (
	CapabilityCopy         Capability = "copy"
	CapabilityMove         Capability = "move"
	CapabilityDirectory    Capability = "directory"
	CapabilityURL          Capability = "url"
	CapabilityTemporaryURL Capability = "temporary_url"
	CapabilityVisibility   Capability = "visibility"
)

type CapabilitySet map[Capability]struct{}

func NewCapabilitySet(caps ...Capability) CapabilitySet {
	set := make(CapabilitySet, len(caps))
	for _, cap := range caps {
		set[cap] = struct{}{}
	}
	return set
}

func (s CapabilitySet) Has(cap Capability) bool {
	_, ok := s[cap]
	return ok
}

func (s CapabilitySet) Clone() CapabilitySet {
	if s == nil {
		return nil
	}
	clone := make(CapabilitySet, len(s))
	for cap := range s {
		clone[cap] = struct{}{}
	}
	return clone
}

func (s CapabilitySet) Without(caps ...Capability) CapabilitySet {
	clone := s.Clone()
	for _, cap := range caps {
		delete(clone, cap)
	}
	return clone
}

type Adapter interface {
	Write(ctx context.Context, path string, r io.Reader, opts WriteOptions) error
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	Exists(ctx context.Context, path string) (bool, error)
	Stat(ctx context.Context, path string) (FileInfo, error)
	ListPage(ctx context.Context, prefix string, opts ListOptions) (Page, error)
	Capabilities() CapabilitySet
}

type Copier interface {
	Copy(ctx context.Context, src string, dst string) error
}

type Mover interface {
	Move(ctx context.Context, src string, dst string) error
}

type DirectoryManager interface {
	MakeDirectory(ctx context.Context, dir string, opts DirectoryOptions) error
	DeleteDirectory(ctx context.Context, dir string) error
}

type DirectorySemantics string

const (
	DirectoryReal        DirectorySemantics = "real"
	DirectoryPrefixOnly  DirectorySemantics = "prefix_only"
	DirectoryUnsupported DirectorySemantics = "unsupported"
)

type DirectorySemanticsProvider interface {
	DirectorySemantics() DirectorySemantics
}

type URLGenerator interface {
	URL(ctx context.Context, path string) (string, error)
}

type TemporaryURLGenerator interface {
	TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts URLOptions) (string, error)
}

type VisibilityController interface {
	GetVisibility(ctx context.Context, path string) (Visibility, error)
	SetVisibility(ctx context.Context, path string, visibility Visibility) error
}
