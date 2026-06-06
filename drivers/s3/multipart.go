package s3

import (
	"context"
	"fmt"
	"io"
	"sort"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/duolabmeng6/go-filesystem"
)

const (
	s3MultipartMinPartSize = int64(5 * 1024 * 1024)
	s3MultipartMaxPartSize = int64(5 * 1024 * 1024 * 1024)
	s3MultipartMaxParts    = 10000
)

func (a *Adapter) CreateMultipartUpload(ctx context.Context, path string, opts filesystem.WriteOptions) (filesystem.MultipartUpload, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.MultipartUpload{}, err
	}
	if !opts.Visibility.Valid() {
		return filesystem.MultipartUpload{}, filesystem.ErrInvalidVisibility
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	if a.disableACL && opts.Visibility != "" {
		return filesystem.MultipartUpload{}, filesystem.ErrUnsupported
	}
	if !opts.Overwrite {
		exists, err := a.Exists(ctx, path)
		if err != nil {
			return filesystem.MultipartUpload{}, err
		}
		if exists {
			return filesystem.MultipartUpload{}, filesystem.ErrAlreadyExists
		}
	}
	input := &awss3.CreateMultipartUploadInput{
		Bucket: &a.bucket,
		Key:    &path,
	}
	if !a.disableACL {
		if acl := objectACL(visibility); acl != "" {
			input.ACL = acl
		}
	}
	output, err := a.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return filesystem.MultipartUpload{}, mapError(err)
	}
	return filesystem.MultipartUpload{
		UploadID:    awsString(output.UploadId),
		Path:        path,
		MinPartSize: s3MultipartMinPartSize,
		MaxPartSize: s3MultipartMaxPartSize,
		MaxParts:    s3MultipartMaxParts,
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
	input := &awss3.UploadPartInput{
		Bucket:     &a.bucket,
		Key:        &path,
		UploadId:   &uploadID,
		PartNumber: &part,
		Body:       r,
	}
	if size >= 0 {
		input.ContentLength = &size
	}
	output, err := a.client.UploadPart(ctx, input)
	if err != nil {
		return filesystem.MultipartUploadPart{}, mapError(err)
	}
	return filesystem.MultipartUploadPart{
		PartNumber: partNumber,
		ETag:       awsString(output.ETag),
		Size:       size,
	}, nil
}

func (a *Adapter) ListMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]filesystem.MultipartUploadPart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parts := make([]filesystem.MultipartUploadPart, 0)
	var marker *string
	for {
		output, err := a.client.ListParts(ctx, &awss3.ListPartsInput{
			Bucket:           &a.bucket,
			Key:              &path,
			UploadId:         &uploadID,
			MaxParts:         int32Ptr(1000),
			PartNumberMarker: marker,
		})
		if err != nil {
			return nil, mapError(err)
		}
		for _, part := range output.Parts {
			partNumber := 0
			if part.PartNumber != nil {
				partNumber = int(*part.PartNumber)
			}
			size := int64(0)
			if part.Size != nil {
				size = *part.Size
			}
			parts = append(parts, filesystem.MultipartUploadPart{
				PartNumber: partNumber,
				ETag:       awsString(part.ETag),
				Size:       size,
			})
		}
		if output.IsTruncated == nil || !*output.IsTruncated {
			break
		}
		marker = output.NextPartNumberMarker
		if marker == nil || *marker == "" {
			break
		}
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
		exists, err := a.Exists(ctx, path)
		if err != nil {
			return err
		}
		if exists {
			return filesystem.ErrAlreadyExists
		}
	}
	sort.Slice(parts, func(i, j int) bool {
		return parts[i].PartNumber < parts[j].PartNumber
	})
	completed := make([]types.CompletedPart, 0, len(parts))
	for _, part := range parts {
		partNumber := int32(part.PartNumber)
		completed = append(completed, types.CompletedPart{
			ETag:       stringPtr(part.ETag),
			PartNumber: &partNumber,
		})
	}
	input := &awss3.CompleteMultipartUploadInput{
		Bucket:   &a.bucket,
		Key:      &path,
		UploadId: &uploadID,
		MultipartUpload: &types.CompletedMultipartUpload{
			Parts: completed,
		},
	}
	_, err := a.client.CompleteMultipartUpload(ctx, input)
	return mapError(err)
}

func (a *Adapter) AbortMultipartUpload(ctx context.Context, path string, uploadID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := a.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket:   &a.bucket,
		Key:      &path,
		UploadId: &uploadID,
	})
	return mapError(err)
}
