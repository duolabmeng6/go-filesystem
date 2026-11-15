package local

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	DataFile   string `json:"data_file,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
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
	lock, err := a.lockMultipartSession(ctx, uploadID, false)
	if err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	defer lock.Close()
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	if partNumber < 1 || partNumber > localMultipartMaxParts {
		return filesystem.MultipartUploadPart{}, fmt.Errorf("%w: invalid part number", filesystem.ErrInvalidPath)
	}
	partLock, err := a.lockMultipartPart(ctx, uploadID, partNumber)
	if err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	defer partLock.Close()
	var oldMeta multipartPartMetadata
	var oldPath string
	if err := readJSON(a.multipartPartMetaPath(uploadID, partNumber), &oldMeta); err == nil {
		oldPath, err = a.multipartDataPath(uploadID, partNumber, oldMeta.DataFile)
		if err != nil || oldMeta.PartNumber != partNumber {
			return filesystem.MultipartUploadPart{}, fmt.Errorf("%w: invalid multipart part metadata", filesystem.ErrInvalidPath)
		}
	} else if !errors.Is(err, filesystem.ErrNotFound) {
		return filesystem.MultipartUploadPart{}, err
	}
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
	contentHash := sha256.New()
	written, err := copyWithContext(ctx, io.MultiWriter(temp, hash, contentHash), r)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return filesystem.MultipartUploadPart{}, mapLocalError(err)
	}
	if size >= 0 && written != size {
		return filesystem.MultipartUploadPart{}, fmt.Errorf("%w: part size mismatch", filesystem.ErrInvalidPath)
	}
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	part := filesystem.MultipartUploadPart{
		PartNumber: partNumber,
		ETag:       hex.EncodeToString(hash.Sum(nil)),
		Size:       written,
	}
	// Each published manifest points at an immutable file. Replacing a part is
	// therefore one atomic manifest rename, including across processes.
	partMeta := multipartPartMetadata{PartNumber: part.PartNumber, ETag: part.ETag, Size: part.Size, DataFile: filepath.Base(tempName), SHA256: hex.EncodeToString(contentHash.Sum(nil))}
	if err := writeJSONAtomic(a.multipartPartMetaPath(uploadID, partNumber), partMeta); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	cleanup = false
	if oldPath != "" {
		if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return part, fmt.Errorf("%w: part uploaded but previous version cleanup failed: %w", filesystem.ErrPartialFailure, err)
		}
	}
	return part, nil
}

func (a *Adapter) ListMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := a.lockMultipartSession(ctx, uploadID, false)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return a.listMultipartUploadParts(ctx, path, uploadID)
}

func (a *Adapter) listMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]filesystem.MultipartUploadPart, error) {
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
		parts = append(parts, filesystem.MultipartUploadPart{PartNumber: meta.PartNumber, ETag: meta.ETag, Size: meta.Size})
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
	lock, err := a.lockMultipartSession(ctx, uploadID, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err := a.readMultipartMetadata(path, uploadID); err != nil {
		return err
	}
	if len(parts) == 0 {
		var err error
		parts, err = a.listMultipartUploadParts(ctx, path, uploadID)
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
	metas := make([]multipartPartMetadata, 0, len(parts))
	for i, part := range parts {
		if part.PartNumber < 1 || part.PartNumber > localMultipartMaxParts || (i > 0 && parts[i-1].PartNumber == part.PartNumber) {
			return fmt.Errorf("%w: invalid or duplicate part number", filesystem.ErrInvalidPath)
		}
		var meta multipartPartMetadata
		if err := readJSON(a.multipartPartMetaPath(uploadID, part.PartNumber), &meta); err != nil {
			return err
		}
		if meta.PartNumber != part.PartNumber || meta.Size < 0 || meta.ETag == "" || (part.ETag != "" && part.ETag != meta.ETag) || (part.Size != 0 && part.Size != meta.Size) {
			return fmt.Errorf("%w: multipart part metadata mismatch", filesystem.ErrInvalidPath)
		}
		if _, err := a.multipartDataPath(uploadID, part.PartNumber, meta.DataFile); err != nil {
			return err
		}
		metas = append(metas, meta)
	}
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var pipeErr error
		for _, meta := range metas {
			partPath, _ := a.multipartDataPath(uploadID, meta.PartNumber, meta.DataFile)
			file, err := os.Open(partPath)
			if err != nil {
				pipeErr = mapLocalError(err)
				break
			}
			hash := md5.New()
			contentHash := sha256.New()
			written, copyErr := copyWithContext(ctx, io.MultiWriter(pw, hash, contentHash), file)
			closeErr := file.Close()
			if copyErr != nil {
				pipeErr = copyErr
				break
			}
			if closeErr != nil {
				pipeErr = mapLocalError(closeErr)
				break
			}
			if written != meta.Size || hex.EncodeToString(hash.Sum(nil)) != meta.ETag || (meta.SHA256 != "" && hex.EncodeToString(contentHash.Sum(nil)) != meta.SHA256) {
				pipeErr = fmt.Errorf("%w: multipart part content mismatch", filesystem.ErrInvalidPath)
				break
			}
		}
		_ = pw.CloseWithError(pipeErr)
	}()
	writeErr := a.Write(ctx, path, pr, opts)
	closeErr := pr.Close()
	<-done
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
	lock, err := a.lockMultipartSession(ctx, uploadID, true)
	if err != nil {
		return err
	}
	defer lock.Close()
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

func (a *Adapter) multipartDataPath(uploadID string, partNumber int, dataFile string) (string, error) {
	if dataFile == "" {
		return a.multipartPartPath(uploadID, partNumber), nil
	}
	if filepath.Base(dataFile) != dataFile || !strings.HasPrefix(dataFile, fmt.Sprintf("part-%06d-", partNumber)) {
		return "", fmt.Errorf("%w: invalid multipart part data file", filesystem.ErrInvalidPath)
	}
	return filepath.Join(a.multipartDir(uploadID), dataFile), nil
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

func writeJSONAtomic(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".part-meta-*")
	if err != nil {
		return mapLocalError(err)
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return mapLocalError(err)
	}
	if err := temp.Close(); err != nil {
		return mapLocalError(err)
	}
	return mapLocalError(os.Rename(temp.Name(), path))
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
