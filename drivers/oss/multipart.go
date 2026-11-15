package oss

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/duolabmeng6/go-filesystem"
)

const (
	ossMultipartMinPartSize = int64(100 * 1024)
	ossMultipartMaxPartSize = int64(5 * 1024 * 1024 * 1024)
	ossMultipartMaxParts    = 10000
)

func (a *Adapter) CreateMultipartUpload(ctx context.Context, path string, opts filesystem.WriteOptions) (filesystem.MultipartUpload, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUpload{}, err
	}
	if !opts.Visibility.Valid() {
		return filesystem.MultipartUpload{}, filesystem.ErrInvalidVisibility
	}
	if !opts.Overwrite {
		if err := a.checkTargetAbsent(ctx, path); err != nil {
			return filesystem.MultipartUpload{}, err
		}
	}
	request := &aliyunoss.InitiateMultipartUploadRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	}
	if !opts.Overwrite {
		request.ForbidOverwrite = aliyunoss.Ptr("true")
	}
	result, err := a.client.InitiateMultipartUpload(ctx, request)
	if err != nil {
		return filesystem.MultipartUpload{}, mapError(err)
	}
	return filesystem.MultipartUpload{
		UploadID:    aliyunoss.ToString(result.UploadId),
		Path:        path,
		MinPartSize: ossMultipartMinPartSize,
		MaxPartSize: ossMultipartMaxPartSize,
		MaxParts:    ossMultipartMaxParts,
	}, nil
}

func (a *Adapter) UploadPart(ctx context.Context, path string, uploadID string, partNumber int, r io.Reader, size int64) (filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUploadPart{}, err
	}
	if r == nil {
		return filesystem.MultipartUploadPart{}, fmt.Errorf("%w: nil reader", filesystem.ErrInvalidPath)
	}
	part := int32(partNumber)
	request := &aliyunoss.UploadPartRequest{
		Bucket:     aliyunoss.Ptr(a.bucket),
		Key:        aliyunoss.Ptr(path),
		UploadId:   aliyunoss.Ptr(uploadID),
		PartNumber: part,
		Body:       r,
	}
	if size >= 0 {
		request.ContentLength = aliyunoss.Ptr(size)
	}
	result, err := a.client.UploadPart(ctx, request)
	if err != nil {
		return filesystem.MultipartUploadPart{}, mapError(err)
	}
	return filesystem.MultipartUploadPart{
		PartNumber: partNumber,
		ETag:       aliyunoss.ToString(result.ETag),
		Size:       size,
	}, nil
}

func (a *Adapter) ListMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parts := make([]filesystem.MultipartUploadPart, 0)
	var marker int32
	seen := make(map[int32]struct{})
	for {
		result, err := a.client.ListParts(ctx, &aliyunoss.ListPartsRequest{
			Bucket:           aliyunoss.Ptr(a.bucket),
			Key:              aliyunoss.Ptr(path),
			UploadId:         aliyunoss.Ptr(uploadID),
			MaxParts:         1000,
			PartNumberMarker: marker,
		})
		if err != nil {
			return nil, mapError(err)
		}
		if result == nil {
			return nil, fmt.Errorf("oss list parts: missing response")
		}
		pageMax := marker
		for _, part := range result.Parts {
			if part.PartNumber < 1 || part.PartNumber > ossMultipartMaxParts || part.Size < 0 || part.ETag == nil || strings.TrimSpace(*part.ETag) == "" {
				return nil, fmt.Errorf("oss list parts: invalid or missing part metadata")
			}
			if _, exists := seen[part.PartNumber]; exists {
				return nil, fmt.Errorf("oss list parts: duplicate part number %d", part.PartNumber)
			}
			if part.PartNumber <= marker {
				return nil, fmt.Errorf("oss list parts: part number does not advance past marker")
			}
			seen[part.PartNumber] = struct{}{}
			if part.PartNumber > pageMax {
				pageMax = part.PartNumber
			}
			parts = append(parts, filesystem.MultipartUploadPart{
				PartNumber: int(part.PartNumber),
				ETag:       aliyunoss.ToString(part.ETag),
				Size:       part.Size,
			})
		}
		if !result.IsTruncated {
			break
		}
		if result.NextPartNumberMarker <= marker || result.NextPartNumberMarker > ossMultipartMaxParts || result.NextPartNumberMarker != pageMax {
			return nil, fmt.Errorf("oss list parts: invalid next part marker")
		}
		marker = result.NextPartNumberMarker
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
	if !opts.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	if len(parts) == 0 {
		return fmt.Errorf("%w: no uploaded parts", filesystem.ErrInvalidPath)
	}
	if !opts.Overwrite {
		if err := a.checkTargetAbsent(ctx, path); err != nil {
			return err
		}
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	completed := make([]aliyunoss.UploadPart, 0, len(parts))
	for _, part := range parts {
		completed = append(completed, aliyunoss.UploadPart{
			PartNumber: int32(part.PartNumber),
			ETag:       aliyunoss.Ptr(part.ETag),
		})
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	request := &aliyunoss.CompleteMultipartUploadRequest{
		Bucket:   aliyunoss.Ptr(a.bucket),
		Key:      aliyunoss.Ptr(path),
		UploadId: aliyunoss.Ptr(uploadID),
		CompleteMultipartUpload: &aliyunoss.CompleteMultipartUpload{
			Parts: completed,
		},
		Acl: objectACL(visibility),
	}
	if !opts.Overwrite {
		request.ForbidOverwrite = aliyunoss.Ptr("true")
	}
	_, err := a.client.CompleteMultipartUpload(ctx, request)
	return mapError(err)
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, path string, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := a.client.AbortMultipartUpload(ctx, &aliyunoss.AbortMultipartUploadRequest{
		Bucket:   aliyunoss.Ptr(a.bucket),
		Key:      aliyunoss.Ptr(path),
		UploadId: aliyunoss.Ptr(uploadID),
	})
	return mapError(err)
}
