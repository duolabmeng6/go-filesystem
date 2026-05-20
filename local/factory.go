package local

import (
	"context"

	"github.com/duolabmeng6/go-filesystem"
)

func NewFactory() filesystem.DriverFactory {
	return func(ctx context.Context, config filesystem.DiskConfig) (filesystem.Adapter, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return New(Config{
			Root:       config.Root,
			BaseURL:    config.BaseURL,
			Visibility: config.Visibility,
		})
	}
}

func NewDisk(config Config) (*filesystem.Disk, error) {
	adapter, err := New(config)
	if err != nil {
		return nil, err
	}
	return filesystem.NewDisk(adapter), nil
}
