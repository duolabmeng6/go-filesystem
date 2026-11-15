//go:build !darwin && !linux

package local

import (
	"context"

	"github.com/duolabmeng6/go-filesystem"
)

const noReplaceSupported = false

func (a *Adapter) copyNoReplace(context.Context, string, string) error {
	return filesystem.ErrUnsupported
}
func (a *Adapter) moveNoReplace(context.Context, string, string) error {
	return filesystem.ErrUnsupported
}
