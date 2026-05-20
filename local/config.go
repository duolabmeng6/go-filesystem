package local

import (
	"context"
	"io/fs"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

type TemporaryURLBuilder func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error)

type Permissions struct {
	FilePublic  fs.FileMode
	FilePrivate fs.FileMode
	DirPublic   fs.FileMode
	DirPrivate  fs.FileMode
}

func DefaultPermissions() Permissions {
	return Permissions{
		FilePublic:  0o644,
		FilePrivate: 0o600,
		DirPublic:   0o755,
		DirPrivate:  0o700,
	}
}

type Config struct {
	Root                string
	BaseURL             string
	Visibility          filesystem.Visibility
	Permissions         Permissions
	TemporaryURLBuilder TemporaryURLBuilder
}

func (c Config) withDefaults() (Config, error) {
	if c.Root == "" {
		c.Root = "./storage"
	}
	if c.Visibility == "" {
		c.Visibility = filesystem.VisibilityPrivate
	}
	if !c.Visibility.Valid() {
		return c, filesystem.ErrInvalidVisibility
	}
	defaults := DefaultPermissions()
	if c.Permissions.FilePublic == 0 {
		c.Permissions.FilePublic = defaults.FilePublic
	}
	if c.Permissions.FilePrivate == 0 {
		c.Permissions.FilePrivate = defaults.FilePrivate
	}
	if c.Permissions.DirPublic == 0 {
		c.Permissions.DirPublic = defaults.DirPublic
	}
	if c.Permissions.DirPrivate == 0 {
		c.Permissions.DirPrivate = defaults.DirPrivate
	}
	return c, nil
}
