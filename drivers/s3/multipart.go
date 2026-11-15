package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
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
	previousMarker := 0
	seen := make(map[int]struct{})
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
		if output == nil || output.IsTruncated == nil {
			return nil, fmt.Errorf("s3 list parts: missing pagination status")
		}
		pageMax := previousMarker
		for _, part := range output.Parts {
			if part.PartNumber == nil || *part.PartNumber < 1 || *part.PartNumber > s3MultipartMaxParts || part.Size == nil || *part.Size < 0 || part.ETag == nil || strings.TrimSpace(*part.ETag) == "" {
				return nil, fmt.Errorf("s3 list parts: invalid or missing part metadata")
			}
			partNumber := int(*part.PartNumber)
			if partNumber <= previousMarker {
				return nil, fmt.Errorf("s3 list parts: part number does not advance past marker")
			}
			if _, exists := seen[partNumber]; exists {
				return nil, fmt.Errorf("s3 list parts: duplicate part number %d", partNumber)
			}
			seen[partNumber] = struct{}{}
			if partNumber > pageMax {
				pageMax = partNumber
			}
			parts = append(parts, filesystem.MultipartUploadPart{
				PartNumber: partNumber,
				ETag:       *part.ETag,
				Size:       *part.Size,
			})
		}
		if !*output.IsTruncated {
			break
		}
		if output.NextPartNumberMarker == nil {
			return nil, fmt.Errorf("s3 list parts: missing next part marker")
		}
		nextMarker, err := strconv.Atoi(*output.NextPartNumberMarker)
		if err != nil || nextMarker <= previousMarker || nextMarker > s3MultipartMaxParts || nextMarker != pageMax {
			return nil, fmt.Errorf("s3 list parts: invalid next part marker")
		}
		previousMarker = nextMarker
		marker = output.NextPartNumberMarker
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
	if !opts.Overwrite {
		input.IfNoneMatch = stringPtr("*")
	}
	_, err := a.client.CompleteMultipartUpload(ctx, input)
	var apiErr smithy.APIError
	if !opts.Overwrite && errors.As(err, &apiErr) && apiErr.ErrorCode() == "ConditionalRequestConflict" {
		return fmt.Errorf("%w: %w", filesystem.ErrMultipartConflict, err)
	}
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
