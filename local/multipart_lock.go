package local

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

// Lock files use a bounded set of stable slots: deleting a lock file while
// another process waits on it would allow separate locks for the same upload.
func (a *Adapter) lockMultipartSession(ctx context.Context, uploadID string, exclusive bool) (*os.File, error) {
	if strings.TrimSpace(uploadID) == "" {
		return nil, fmt.Errorf("%w: upload id is required", filesystem.ErrInvalidPath)
	}
	return a.lockMultipartSlot(ctx, "session", a.root+"\x00"+uploadID, exclusive)
}

func (a *Adapter) lockMultipartPart(ctx context.Context, uploadID string, partNumber int) (*os.File, error) {
	return a.lockMultipartSlot(ctx, "part", fmt.Sprintf("%s\x00%s:%d", a.root, uploadID, partNumber), true)
}

func (a *Adapter) lockMultipartSlot(ctx context.Context, class, key string, exclusive bool) (*os.File, error) {
	dir := filepath.Join(os.TempDir(), "go-filesystem-multipart-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, mapLocalError(err)
	}
	slot := sha256.Sum256([]byte(key))
	file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf(".%s-lock-%02x", class, slot[0])), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, mapLocalError(err)
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = file.Close()
			return nil, err
		}
		locked, err := tryMultipartLock(file, exclusive)
		if err != nil {
			_ = file.Close()
			return nil, mapLocalError(err)
		}
		if locked {
			return file, nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
