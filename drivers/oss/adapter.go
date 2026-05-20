package oss

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

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/duolabmeng6/go-filesystem"
)

const defaultPageSize = 1000

type Adapter struct {
	client       Client
	bucket       string
	baseURL      string
	urlEnabled   bool
	visibility   filesystem.Visibility
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
	request := &aliyunoss.PutObjectRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
		Body:   r,
	}
	if !opts.Overwrite {
		request.ForbidOverwrite = aliyunoss.Ptr("true")
	}
	visibility := a.visibility
	if opts.Visibility != "" {
		visibility = opts.Visibility
	}
	request.Acl = objectACL(visibility)
	_, err := a.client.PutObject(ctx, request)
	return mapError(err)
}

func (a *Adapter) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := a.client.GetObject(ctx, &aliyunoss.GetObjectRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	})
	if err != nil {
		return nil, mapError(err)
	}
	if result.Body == nil {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return result.Body, nil
}

func (a *Adapter) Delete(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := a.head(ctx, path); err != nil {
		return err
	}
	_, err := a.client.DeleteObject(ctx, &aliyunoss.DeleteObjectRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	})
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
	result, err := a.head(ctx, path)
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	modified := time.Time{}
	if result.LastModified != nil {
		modified = *result.LastModified
	}
	return filesystem.FileInfo{
		Path:         path,
		Size:         result.ContentLength,
		LastModified: modified,
		IsDir:        false,
	}, nil
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
	request := &aliyunoss.ListObjectsV2Request{
		Bucket:            aliyunoss.Ptr(a.bucket),
		Prefix:            aliyunoss.Ptr(listPrefix),
		MaxKeys:           int32(pageSize),
		ContinuationToken: stringPtrIfNotEmpty(opts.Cursor),
	}
	if !opts.Recursive {
		request.Delimiter = aliyunoss.Ptr("/")
	}
	result, err := a.client.ListObjectsV2(ctx, request)
	if err != nil {
		return filesystem.Page{}, mapError(err)
	}
	items := make([]filesystem.Entry, 0, len(result.Contents)+len(result.CommonPrefixes))
	for _, object := range result.Contents {
		key := aliyunoss.ToString(object.Key)
		if key == "" || key == listPrefix {
			continue
		}
		modified := time.Time{}
		if object.LastModified != nil {
			modified = *object.LastModified
		}
		items = append(items, filesystem.Entry{
			Path:         key,
			Type:         filesystem.EntryFile,
			Size:         object.Size,
			LastModified: modified,
		})
	}
	for _, commonPrefix := range result.CommonPrefixes {
		path := strings.TrimSuffix(aliyunoss.ToString(commonPrefix.Prefix), "/")
		if path == "" {
			continue
		}
		items = append(items, filesystem.Entry{
			Path: path,
			Type: filesystem.EntryDirectory,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})
	return filesystem.Page{
		Items:      items,
		NextCursor: aliyunoss.ToString(result.NextContinuationToken),
	}, nil
}

func (a *Adapter) Capabilities() filesystem.CapabilitySet {
	return a.capabilities.Clone()
}

func (a *Adapter) Copy(ctx context.Context, src string, dst string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := a.client.CopyObject(ctx, &aliyunoss.CopyObjectRequest{
		Bucket:       aliyunoss.Ptr(a.bucket),
		Key:          aliyunoss.Ptr(dst),
		SourceBucket: aliyunoss.Ptr(a.bucket),
		SourceKey:    aliyunoss.Ptr(src),
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
	request := &aliyunoss.GetObjectRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	}
	applyOSSURLParameters(request, opts.Parameters)
	result, err := a.client.Presign(ctx, request, aliyunoss.PresignExpiration(expiresAt))
	if err != nil {
		return "", mapError(err)
	}
	return result.URL, nil
}

func applyOSSURLParameters(request *aliyunoss.GetObjectRequest, parameters map[string]string) {
	if request == nil {
		return
	}
	for key, value := range parameters {
		switch strings.ToLower(key) {
		case "response-cache-control":
			request.ResponseCacheControl = aliyunoss.Ptr(value)
		case "response-content-disposition":
			request.ResponseContentDisposition = aliyunoss.Ptr(value)
		case "response-content-encoding":
			request.ResponseContentEncoding = aliyunoss.Ptr(value)
		case "response-content-language":
			request.ResponseContentLanguage = aliyunoss.Ptr(value)
		case "response-content-type":
			request.ResponseContentType = aliyunoss.Ptr(value)
		case "response-expires":
			request.ResponseExpires = aliyunoss.Ptr(value)
		case "x-oss-process":
			request.Process = aliyunoss.Ptr(value)
		}
	}
}

func (a *Adapter) GetVisibility(ctx context.Context, path string) (filesystem.Visibility, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, err := a.client.GetObjectAcl(ctx, &aliyunoss.GetObjectAclRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	})
	if err != nil {
		return "", mapError(err)
	}
	switch aliyunoss.ToString(result.ACL) {
	case string(aliyunoss.ObjectACLPublicRead), string(aliyunoss.ObjectACLPublicReadWrite):
		return filesystem.VisibilityPublic, nil
	case string(aliyunoss.ObjectACLPrivate):
		return filesystem.VisibilityPrivate, nil
	case "", string(aliyunoss.ObjectACLDefault):
		return a.visibility, nil
	default:
		return "", filesystem.ErrUnsupported
	}
}

func (a *Adapter) SetVisibility(ctx context.Context, path string, visibility filesystem.Visibility) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if visibility == "" || !visibility.Valid() {
		return filesystem.ErrInvalidVisibility
	}
	_, err := a.client.PutObjectAcl(ctx, &aliyunoss.PutObjectAclRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
		Acl:    objectACL(visibility),
	})
	return mapError(err)
}

func (a *Adapter) MimeType(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result, err := a.head(ctx, path)
	if err != nil {
		return "", err
	}
	if result.ContentType == nil {
		return "application/octet-stream", nil
	}
	return *result.ContentType, nil
}

func (a *Adapter) head(ctx context.Context, path string) (*aliyunoss.HeadObjectResult, error) {
	result, err := a.client.HeadObject(ctx, &aliyunoss.HeadObjectRequest{
		Bucket: aliyunoss.Ptr(a.bucket),
		Key:    aliyunoss.Ptr(path),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return result, nil
}

func objectACL(visibility filesystem.Visibility) aliyunoss.ObjectACLType {
	if visibility == filesystem.VisibilityPublic {
		return aliyunoss.ObjectACLPublicRead
	}
	return aliyunoss.ObjectACLPrivate
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var serviceErr *aliyunoss.ServiceError
	if errors.As(err, &serviceErr) {
		switch serviceErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %s", filesystem.ErrNotFound, serviceErr.Code)
		case http.StatusPreconditionFailed:
			return fmt.Errorf("%w: %s", filesystem.ErrAlreadyExists, serviceErr.Code)
		}
		switch serviceErr.Code {
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return fmt.Errorf("%w: %s", filesystem.ErrNotFound, serviceErr.Code)
		case "FileAlreadyExists", "ObjectAlreadyExists":
			return fmt.Errorf("%w: %s", filesystem.ErrAlreadyExists, serviceErr.Code)
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

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return aliyunoss.Ptr(value)
}
