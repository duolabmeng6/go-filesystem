package local

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/duolabmeng6/go-filesystem"
)

const (
	localMultipartMinPartSize = int64(1)
	localMultipartMaxPartSize = int64(5 * 1024 * 1024 * 1024)
	localMultipartMaxParts    = 10000
)

type multipartMetadata struct {
	Path       string                `json:"path"`
	UploadID   string                `json:"upload_id"`
	Visibility filesystem.Visibility `json:"visibility,omitempty"`
	Overwrite  bool                  `json:"overwrite"`
}

type multipartPartMetadata struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

func (a *Adapter) CreateMultipartUpload(ctx context.Context, path string, opts filesystem.WriteOptions) (filesystem.MultipartUpload, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUpload{}, err
	}
	if !opts.Visibility.Valid() {
		return filesystem.MultipartUpload{}, filesystem.ErrInvalidVisibility
	}
	target, err := a.targetPath(path)
	if err != nil {
		return filesystem.MultipartUpload{}, err
	}
	parent := filepath.Dir(target)
	if err := a.checkExistingPrefix(parent); err != nil {
		return filesystem.MultipartUpload{}, err
	}
	if err := a.ensureTargetWritable(target, opts.Overwrite); err != nil {
		return filesystem.MultipartUpload{}, err
	}
	uploadID, err := randomUploadID()
	if err != nil {
		return filesystem.MultipartUpload{}, err
	}
	dir := a.multipartDir(uploadID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return filesystem.MultipartUpload{}, mapLocalError(err)
	}
	meta := multipartMetadata{
		Path:       path,
		UploadID:   uploadID,
		Visibility: opts.Visibility,
		Overwrite:  opts.Overwrite,
	}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		_ = os.RemoveAll(dir)
		return filesystem.MultipartUpload{}, err
	}
	return filesystem.MultipartUpload{
		UploadID:    uploadID,
		Path:        path,
		MinPartSize: localMultipartMinPartSize,
		MaxPartSize: localMultipartMaxPartSize,
		MaxParts:    localMultipartMaxParts,
	}, nil
}

func (a *Adapter) UploadPart(ctx context.Context, path string, uploadID string, partNumber int, r io.Reader, size int64) (filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	partPath := a.multipartPartPath(uploadID, partNumber)
	temp, err := os.CreateTemp(a.multipartDir(uploadID), fmt.Sprintf("part-%06d-", partNumber))
	if err != nil {
		return filesystem.MultipartUploadPart{}, mapLocalError(err)
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	hash := md5.New()
	written, err := copyWithContext(ctx, io.MultiWriter(temp, hash), r)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return filesystem.MultipartUploadPart{}, mapLocalError(err)
	}
	if size >= 0 && written != size {
		return filesystem.MultipartUploadPart{}, fmt.Errorf("%w: part size mismatch", filesystem.ErrInvalidPath)
	}
	if err := os.Rename(tempName, partPath); err != nil {
		return filesystem.MultipartUploadPart{}, mapLocalError(err)
	}
	cleanup = false
	part := filesystem.MultipartUploadPart{
		PartNumber: partNumber,
		ETag:       hex.EncodeToString(hash.Sum(nil)),
		Size:       written,
	}
	partMeta := multipartPartMetadata(part)
	if err := writeJSON(a.multipartPartMetaPath(uploadID, partNumber), partMeta); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	return part, nil
}

func (a *Adapter) ListMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(a.multipartDir(uploadID))
	if err != nil {
		return nil, mapLocalError(err)
	}
	parts := make([]filesystem.MultipartUploadPart, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !strings.HasPrefix(entry.Name(), "part-") {
			continue
		}
		var meta multipartPartMetadata
		if err := readJSON(filepath.Join(a.multipartDir(uploadID), entry.Name()), &meta); err != nil {
			return nil, err
		}
		parts = append(parts, filesystem.MultipartUploadPart(meta))
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	return parts, nil
}

func (a *Adapter) CompleteMultipartUpload(ctx context.Context, path string, uploadID string, parts []filesystem.MultipartUploadPart, opts filesystem.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return err
	}
	if len(parts) == 0 {
		var err error
		parts, err = a.ListMultipartUploadParts(ctx, path, uploadID)
		if err != nil {
			return err
		}
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: no uploaded parts", filesystem.ErrInvalidPath)
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	pr, pw := io.Pipe()
	go func() {
		var pipeErr error
		for _, part := range parts {
			if part.PartNumber < 1 || part.PartNumber > localMultipartMaxParts {
				pipeErr = fmt.Errorf("%w: invalid part number", filesystem.ErrInvalidPath)
				break
			}
			file, err := os.Open(a.multipartPartPath(uploadID, part.PartNumber))
			if err != nil {
				pipeErr = mapLocalError(err)
				break
			}
			_, copyErr := copyWithContext(ctx, pw, file)
			closeErr := file.Close()
			if copyErr != nil {
				pipeErr = copyErr
				break
			}
			if closeErr != nil {
				pipeErr = mapLocalError(closeErr)
				break
			}
		}
		_ = pw.CloseWithError(pipeErr)
	}()
	writeErr := a.Write(ctx, path, pr, opts)
	closeErr := pr.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return mapLocalError(os.RemoveAll(a.multipartDir(uploadID)))
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, path string, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return err
	}
	return mapLocalError(os.RemoveAll(a.multipartDir(uploadID)))
}

func (a *Adapter) readMultipartMetadata(path string, uploadID string) (multipartMetadata, error) {
	var meta multipartMetadata
	if strings.TrimSpace(uploadID) == "" {
		return meta, fmt.Errorf("%w: upload id is required", filesystem.ErrInvalidPath)
	}
	if err := readJSON(filepath.Join(a.multipartDir(uploadID), "meta.json"), &meta); err != nil {
		return meta, err
	}
	if meta.Path != path || meta.UploadID != uploadID {
		return meta, fmt.Errorf("%w: multipart upload does not match path", filesystem.ErrInvalidPath)
	}
	return meta, nil
}

func (a *Adapter) multipartDir(uploadID string) string {
	rootHash := sha256.Sum256([]byte(a.root))
	return filepath.Join(os.TempDir(), "go-filesystem-multipart", hex.EncodeToString(rootHash[:8]), uploadID)
}

func (a *Adapter) multipartPartPath(uploadID string, partNumber int) string {
	return filepath.Join(a.multipartDir(uploadID), fmt.Sprintf("part-%06d.data", partNumber))
}

func (a *Adapter) multipartPartMetaPath(uploadID string, partNumber int) string {
	return filepath.Join(a.multipartDir(uploadID), fmt.Sprintf("part-%06d.json", partNumber))
}

func randomUploadID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return mapLocalError(os.WriteFile(path, data, 0o600))
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return mapLocalError(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		return fmt.Errorf("%w: invalid multipart metadata", filesystem.ErrInvalidPath)
	}
	return nil
}
