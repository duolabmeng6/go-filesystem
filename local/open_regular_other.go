//go:build !darwin && !linux

package local

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/duolabmeng6/go-filesystem"
)

// Platforms without the no-follow openat implementation retain the existing
// path-based open, followed by a handle type and cancellation check.
func (a *Adapter) openRegularFile(ctx context.Context, _, target string) (io.ReadCloser, error) {
	file, err := os.Open(target)
	if err != nil {
		return nil, mapLocalError(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, mapLocalError(err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: cannot open non-regular file", filesystem.ErrUnsupported)
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
