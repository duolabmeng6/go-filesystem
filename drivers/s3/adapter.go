package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/duolabmeng6/go-filesystem"
)

const defaultPageSize = 1000

type Adapter struct {
	client       Client
	presigner    PresignClient
	bucket       string
	baseURL      string
	urlEnabled   bool
	visibility   filesystem.Visibility
	disableACL   bool
	capabilities filesystem.CapabilitySet
}

func (a *Adapter) Write(ctx context.Context, path string, r io.Reader, opts filesystem.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return fmt.Errorf("%w: nil reader", filesystem.ErrInvalidPath)
	}
	if !opts.Visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	if a.disableACL && opts.Visibility != "" {
		return filesystem.ErrUnsupported
	}
	input := &awss3.PutObjectInput{
		Bucket: &a.bucket,
		Key:    &path,
		Body:   r,
	}
	if !a.disableACL {
		input.ACL = objectACL(visibility)
	}
	if !opts.Overwrite {
		input.IfNoneMatch = stringPtr("*")
	}
	_, err := a.client.PutObject(ctx, input)
	return mapError(err)
}

func (a *Adapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	output, err := a.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: &a.bucket, Key: &path})
	if err != nil {
		return nil, mapError(err)
	}
	if output.Body == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return output.Body, nil
}

func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.head(ctx, path); err != nil {
		return err
	}
	_, err := a.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: &a.bucket, Key: &path})
	return mapError(err)
}

func (a *Adapter) Exists(ctx context.Context, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	_, err := a.head(ctx, path)
	if errors.Is(err, filesystem.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (a *Adapter) Stat(ctx context.Context, path string) (filesystem.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.FileInfo{}, err
	}
	output, err := a.head(ctx, path)
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	modified := time.Time{}
	if output.LastModified != nil {
		modified = *output.LastModified
	}
	size := int64(0)
	if output.ContentLength != nil {
		size = *output.ContentLength
	}
	return filesystem.FileInfo{Path: path, Size: size, LastModified: modified}, nil
}

func (a *Adapter) ListPage(ctx context.Context, prefix string, opts filesystem.ListOptions) (filesystem.Page, error) {
	if err := ctx.Err(); err != nil {
		return filesystem.Page{}, err
	}
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > defaultPageSize {
		pageSize = defaultPageSize
	}
	listPrefix := prefix
	if listPrefix != "" {
		listPrefix += "/"
	}
	input := &awss3.ListObjectsV2Input{
		Bucket:            &a.bucket,
		Prefix:            &listPrefix,
		MaxKeys:           int32Ptr(int32(pageSize)),
		ContinuationToken: stringPtrIfNotEmpty(opts.Cursor),
	}
	if !opts.Recursive {
		input.Delimiter = stringPtr("/")
	}
	output, err := a.client.ListObjectsV2(ctx, input)
	if err != nil {
		return filesystem.Page{}, mapError(err)
	}
	items := make([]filesystem.Entry, 0, len(output.Contents)+len(output.CommonPrefixes))
	for _, object := range output.Contents {
		key := awsString(object.Key)
		if key == "" || key == listPrefix {
			continue
		}
		modified := time.Time{}
		if object.LastModified != nil {
			modified = *object.LastModified
		}
		size := int64(0)
		if object.Size != nil {
			size = *object.Size
		}
		items = append(items, filesystem.Entry{Path: key, Type: filesystem.EntryFile, Size: size, LastModified: modified})
	}
	for _, commonPrefix := range output.CommonPrefixes {
		path := strings.TrimSuffix(awsString(commonPrefix.Prefix), "/")
		if path == "" {
			continue
		}
		items = append(items, filesystem.Entry{Path: path, Type: filesystem.EntryDirectory})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return filesystem.Page{Items: items, NextCursor: awsString(output.NextContinuationToken)}, nil
}

func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	return a.capabilities.Clone()
}

func (a *Adapter) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	copySource := a.bucket + "/" + escapePath(src)
	_, err := a.client.CopyObject(ctx, &awss3.CopyObjectInput{
		Bucket:     &a.bucket,
		Key:        &dst,
		CopySource: &copySource,
	})
	return mapError(err)
}

func (a *Adapter) DirectorySemantics() filesystem.DirectorySemantics {
	return filesystem.DirectoryPrefixOnly
}

func (a *Adapter) URL(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !a.urlEnabled {
		return "", filesystem.ErrUnsupported
	}
	return a.baseURL + "/" + escapePath(path), nil
}

func (a *Adapter) TemporaryURL(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	request := &awss3.GetObjectInput{
		Bucket: &a.bucket,
		Key:    &path,
	}
	applyS3URLParameters(request, opts.Parameters)
	output, err := a.presigner.PresignGetObject(ctx, request, awss3.WithPresignExpires(time.Until(expiresAt)))
	if err != nil {
		return "", mapError(err)
	}
	return output.URL, nil
}

func applyS3URLParameters(request *awss3.GetObjectInput, parameters map[string]string) {
	if request == nil {
		return
	}
	for key, value := range parameters {
		switch strings.ToLower(key) {
		case "response-cache-control":
			request.ResponseCacheControl = stringPtr(value)
		case "response-content-disposition":
			request.ResponseContentDisposition = stringPtr(value)
		case "response-content-encoding":
			request.ResponseContentEncoding = stringPtr(value)
		case "response-content-language":
			request.ResponseContentLanguage = stringPtr(value)
		case "response-content-type":
			request.ResponseContentType = stringPtr(value)
		case "response-expires":
			if parsed, err := http.ParseTime(value); err == nil {
				request.ResponseExpires = &parsed
			}
		}
	}
}

func (a *Adapter) GetVisibility(ctx context.Context, path string) (filesystem.Visibility, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if a.disableACL {
		return "", filesystem.ErrUnsupported
	}
	output, err := a.client.GetObjectAcl(ctx, &awss3.GetObjectAclInput{Bucket: &a.bucket, Key: &path})
	if err != nil {
		return "", mapError(err)
	}
	for _, grant := range output.Grants {
		if grant.Permission == types.PermissionRead && grant.Grantee != nil && grant.Grantee.Type == types.TypeGroup && grant.Grantee.URI != nil && strings.Contains(*grant.Grantee.URI, "AllUsers") {
			return filesystem.VisibilityPublic, nil
		}
	}
	return filesystem.VisibilityPrivate, nil
}

func (a *Adapter) SetVisibility(ctx context.Context, path string, visibility filesystem.Visibility) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if visibility == "" || !visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	if a.disableACL {
		return filesystem.ErrUnsupported
	}
	_, err := a.client.PutObjectAcl(ctx, &awss3.PutObjectAclInput{
		Bucket: &a.bucket,
		Key:    &path,
		ACL:    objectACL(visibility),
	})
	return mapError(err)
}

func (a *Adapter) MimeType(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output, err := a.head(ctx, path)
	if err != nil {
		return "", err
	}
	if output.ContentType == nil {
		return "application/octet-stream", nil
	}
	return *output.ContentType, nil
}

func (a *Adapter) head(ctx context.Context, path string) (*awss3.HeadObjectOutput, error) {
	output, err := a.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: &a.bucket, Key: &path})
	if err != nil {
		return nil, mapError(err)
	}
	return output, nil
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "NoSuchBucket", "404":
			return fmt.Errorf("%w: %s", filesystem.ErrNotFound, apiErr.ErrorCode())
		case "PreconditionFailed", "ConditionalRequestConflict", "ObjectAlreadyExists", "412":
			return fmt.Errorf("%w: %s", filesystem.ErrAlreadyExists, apiErr.ErrorCode())
		}
	}
	var responseErr *awshttp.ResponseError
	if errors.As(err, &responseErr) {
		switch responseErr.HTTPStatusCode() {
		case http.StatusNotFound:
			return filesystem.ErrNotFound
		case http.StatusPreconditionFailed:
			return filesystem.ErrAlreadyExists
		}
	}
	return err
}

func trimBaseURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/")
}

func escapePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func stringPtr(value string) *string { return &value }

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func int32Ptr(value int32) *int32 { return &value }

func awsString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
