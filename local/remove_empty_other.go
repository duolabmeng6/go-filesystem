//go:build !darwin && !linux && !windows

package local

import (
	"context"

	"github.com/duolabmeng6/go-filesystem"
)

func removeEmptyDirectory(ctx context.Context, _, _ string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return filesystem.ErrUnsupported
}
