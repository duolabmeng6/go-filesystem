package oss

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
)

type mockClient struct {
	objects               map[string]mockObject
	multipart             map[string]mockMultipartUpload
	deleteMultipleBatches [][]string
}

type mockObject struct {
	data         []byte
	contentType  string
	lastModified time.Time
	acl          string
}

type mockMultipartUpload struct {
	key   string
	parts map[int32]mockMultipartPart
}

type mockMultipartPart struct {
	data []byte
	etag string
	size int64
}

func newMockClient() *mockClient {
	return &mockClient{
		objects:   map[string]mockObject{},
		multipart: map[string]mockMultipartUpload{},
	}
}

func newTestDisk(t testing.TB) *filesystem.Disk {
	t.Helper()
	disk, err := NewDisk(Config{
		Bucket:     "bucket",
		Region:     "cn-hangzhou",
		BaseURL:    "https://cdn.example.com/files",
		Visibility: filesystem.VisibilityPrivate,
		Client:     newMockClient(),
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	return disk
}

func TestContracts(t *testing.T) {
	filesystemtest.RunObjectContract(t, newTestDisk)
	filesystemtest.RunListContract(t, newTestDisk)
	filesystemtest.RunVisibilityContract(t, newTestDisk)
	filesystemtest.RunPathSafetyContract(t, newTestDisk)
	filesystemtest.RunURLContract(t, func(t testing.TB) *filesystem.Disk {
		t.Helper()
		disk, err := NewDisk(Config{
			Bucket:  "bucket",
			Region:  "cn-hangzhou",
			BaseURL: "/storage",
			Client:  newMockClient(),
		})
		if err != nil {
			t.Fatalf("new disk: %v", err)
		}
		return disk
	})
	filesystemtest.RunTemporaryURLContract(t, newTestDisk)
	filesystemtest.RunMultipartContract(t, newTestDisk)
}

func TestTemporaryURL(t *testing.T) {
	disk := newTestDisk(t)
	got, err := disk.TemporaryURL(context.Background(), "a.txt", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("temporary url: %v", err)
	}
	if !strings.Contains(got, "https://signed.example.com/a.txt") {
		t.Fatalf("got %q", got)
	}
}

func TestTemporaryURLWithOSSProcess(t *testing.T) {
	disk := newTestDisk(t)
	got, err := disk.TemporaryURL(
		context.Background(),
		"a.jpg",
		time.Now().Add(time.Hour),
		filesystem.WithURLParameter("x-oss-process", "image/resize,w_800/quality,q_80"),
	)
	if err != nil {
		t.Fatalf("temporary url: %v", err)
	}
	if !strings.Contains(got, "x-oss-process=image%2Fresize%2Cw_800%2Fquality%2Cq_80") {
		t.Fatalf("expected signed url to include x-oss-process, got %q", got)
	}
}

func TestFactoryUsesOptions(t *testing.T) {
	manager := filesystem.New(filesystem.WithDriver("oss", NewFactory()))
	disk, err := manager.Build(context.Background(), filesystem.DiskConfig{
		Driver:  "oss",
		BaseURL: "https://cdn.example.com",
		Options: map[string]any{
			"bucket":            "bucket",
			"region":            "cn-hangzhou",
			"access_key_id":     "ak",
			"access_key_secret": "sk",
			"use_path_style":    true,
			"disable_ssl":       true,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if disk == nil {
		t.Fatalf("expected disk")
	}
}

func TestConfigDefaultsToOSSObjectACLDefault(t *testing.T) {
	client := newMockClient()
	disk, err := NewDisk(Config{
		Bucket: "bucket",
		Region: "cn-hangzhou",
		Client: client,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	ctx := context.Background()
	if err := disk.Put(ctx, "inherited.txt", []byte("inherited")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := client.objects["inherited.txt"].acl; got != string(aliyunoss.ObjectACLDefault) {
		t.Fatalf("expected default ACL for implicit OSS visibility, got %q", got)
	}
}

func TestGetVisibilityReturnsOSSObjectACLDefault(t *testing.T) {
	client := newMockClient()
	client.objects["inherited.txt"] = mockObject{acl: string(aliyunoss.ObjectACLDefault)}
	disk, err := NewDisk(Config{
		Bucket:     "bucket",
		Region:     "cn-hangzhou",
		Visibility: filesystem.VisibilityPrivate,
		Client:     client,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	visibility, err := disk.GetVisibility(context.Background(), "inherited.txt")
	if err != nil {
		t.Fatalf("get visibility: %v", err)
	}
	if visibility != filesystem.VisibilityDefault {
		t.Fatalf("expected default visibility for OSS default ACL, got %q", visibility)
	}
}

func TestDefaultVisibilityUsesOSSObjectACLDefault(t *testing.T) {
	client := newMockClient()
	disk, err := NewDisk(Config{
		Bucket:     "bucket",
		Region:     "cn-hangzhou",
		Visibility: filesystem.VisibilityDefault,
		Client:     client,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	ctx := context.Background()
	if err := disk.Put(ctx, "downloaded.txt", []byte("downloaded")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := client.objects["downloaded.txt"].acl; got != string(aliyunoss.ObjectACLDefault) {
		t.Fatalf("expected default ACL for write, got %q", got)
	}
	visibility, err := disk.GetVisibility(ctx, "downloaded.txt")
	if err != nil {
		t.Fatalf("get visibility: %v", err)
	}
	if visibility != filesystem.VisibilityDefault {
		t.Fatalf("expected default visibility, got %q", visibility)
	}
	if err := disk.Copy(ctx, "downloaded.txt", "copied.txt"); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := client.objects["copied.txt"].acl; got != string(aliyunoss.ObjectACLDefault) {
		t.Fatalf("expected default ACL for copy, got %q", got)
	}
	upload, err := disk.CreateMultipartUpload(ctx, "multipart.txt")
	if err != nil {
		t.Fatalf("create multipart upload: %v", err)
	}
	part, err := disk.UploadPart(ctx, "multipart.txt", upload.UploadID, 1, strings.NewReader("multipart"), int64(len("multipart")))
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if err := disk.CompleteMultipartUpload(ctx, "multipart.txt", upload.UploadID, []filesystem.MultipartUploadPart{part}); err != nil {
		t.Fatalf("complete multipart upload: %v", err)
	}
	if got := client.objects["multipart.txt"].acl; got != string(aliyunoss.ObjectACLDefault) {
		t.Fatalf("expected default ACL for multipart, got %q", got)
	}
	if err := disk.Put(ctx, "public.txt", []byte("public"), filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
		t.Fatalf("put public: %v", err)
	}
	if got := client.objects["public.txt"].acl; got != string(aliyunoss.ObjectACLPublicRead) {
		t.Fatalf("expected explicit public ACL to override default, got %q", got)
	}
}

func TestDeleteManyUsesOSSBulkDeleteWhenIgnoringMissing(t *testing.T) {
	client := newMockClient()
	disk, err := NewDisk(Config{
		Bucket: "bucket",
		Region: "cn-hangzhou",
		Client: client,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	ctx := context.Background()
	for _, path := range []string{"notes/a.md", "notes/b.md"} {
		if err := disk.Put(ctx, path, []byte(path)); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	if err := disk.DeleteMany(ctx, []string{"notes/a.md", "notes/b.md", "notes/missing.md"}, filesystem.WithIgnoreMissing()); err != nil {
		t.Fatalf("delete many: %v", err)
	}
	if len(client.deleteMultipleBatches) != 1 {
		t.Fatalf("expected one bulk delete request, got %d", len(client.deleteMultipleBatches))
	}
	got := strings.Join(client.deleteMultipleBatches[0], ",")
	if got != "notes/a.md,notes/b.md,notes/missing.md" {
		t.Fatalf("unexpected delete batch: %q", got)
	}
	for _, path := range []string{"notes/a.md", "notes/b.md"} {
		exists, err := disk.Exists(ctx, path)
		if err != nil {
			t.Fatalf("exists %s: %v", path, err)
		}
		if exists {
			t.Fatalf("expected %s to be deleted", path)
		}
	}
}

func TestDeleteDirectoryUsesOSSBulkDeleteWithPathPrefix(t *testing.T) {
	client := newMockClient()
	disk, err := NewDisk(Config{
		Bucket:     "bucket",
		Region:     "cn-hangzhou",
		PathPrefix: "site",
		Client:     client,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	ctx := context.Background()
	for _, path := range []string{"docs/a.md", "docs/nested/b.md"} {
		if err := disk.Put(ctx, path, []byte(path)); err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
	}
	if err := disk.DeleteDirectory(ctx, "docs"); err != nil {
		t.Fatalf("delete directory: %v", err)
	}
	if len(client.deleteMultipleBatches) != 1 {
		t.Fatalf("expected one bulk delete request, got %d", len(client.deleteMultipleBatches))
	}
	got := strings.Join(client.deleteMultipleBatches[0], ",")
	if got != "site/docs/a.md,site/docs/nested/b.md" {
		t.Fatalf("unexpected delete batch: %q", got)
	}
}

func (m *mockClient) PutObject(ctx context.Context, request *aliyunoss.PutObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.PutObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := aliyunoss.ToString(request.Key)
	if request.ForbidOverwrite != nil && *request.ForbidOverwrite == "true" {
		if _, ok := m.objects[key]; ok {
			return nil, serviceError(http.StatusPreconditionFailed, "FileAlreadyExists")
		}
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	acl := string(request.Acl)
	if acl == "" {
		acl = string(aliyunoss.ObjectACLPrivate)
	}
	m.objects[key] = mockObject{
		data:         data,
		contentType:  http.DetectContentType(data),
		lastModified: time.Now().UTC(),
		acl:          acl,
	}
	return &aliyunoss.PutObjectResult{}, nil
}

func (m *mockClient) InitiateMultipartUpload(ctx context.Context, request *aliyunoss.InitiateMultipartUploadRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.InitiateMultipartUploadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := aliyunoss.ToString(request.Key)
	if request.ForbidOverwrite != nil && *request.ForbidOverwrite == "true" {
		if _, ok := m.objects[key]; ok {
			return nil, serviceError(http.StatusPreconditionFailed, "FileAlreadyExists")
		}
	}
	uploadID := "upload-" + string(rune('a'+len(m.multipart)))
	m.multipart[uploadID] = mockMultipartUpload{
		key:   key,
		parts: map[int32]mockMultipartPart{},
	}
	return &aliyunoss.InitiateMultipartUploadResult{
		Bucket:   aliyunoss.Ptr(aliyunoss.ToString(request.Bucket)),
		Key:      aliyunoss.Ptr(key),
		UploadId: aliyunoss.Ptr(uploadID),
	}, nil
}

func (m *mockClient) UploadPart(ctx context.Context, request *aliyunoss.UploadPartRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.UploadPartResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uploadID := aliyunoss.ToString(request.UploadId)
	upload, ok := m.multipart[uploadID]
	if !ok || upload.key != aliyunoss.ToString(request.Key) {
		return nil, serviceError(http.StatusNotFound, "NoSuchUpload")
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	etag := "etag-" + string(rune('a'+request.PartNumber))
	upload.parts[request.PartNumber] = mockMultipartPart{data: data, etag: etag, size: int64(len(data))}
	m.multipart[uploadID] = upload
	return &aliyunoss.UploadPartResult{ETag: aliyunoss.Ptr(etag)}, nil
}

func (m *mockClient) ListParts(ctx context.Context, request *aliyunoss.ListPartsRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.ListPartsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	upload, ok := m.multipart[aliyunoss.ToString(request.UploadId)]
	if !ok || upload.key != aliyunoss.ToString(request.Key) {
		return nil, serviceError(http.StatusNotFound, "NoSuchUpload")
	}
	numbers := make([]int, 0, len(upload.parts))
	for number := range upload.parts {
		numbers = append(numbers, int(number))
	}
	sort.Ints(numbers)
	result := &aliyunoss.ListPartsResult{IsTruncated: false}
	for _, number := range numbers {
		part := upload.parts[int32(number)]
		result.Parts = append(result.Parts, aliyunoss.Part{
			PartNumber: int32(number),
			ETag:       aliyunoss.Ptr(part.etag),
			Size:       part.size,
		})
	}
	return result, nil
}

func (m *mockClient) CompleteMultipartUpload(ctx context.Context, request *aliyunoss.CompleteMultipartUploadRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.CompleteMultipartUploadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := aliyunoss.ToString(request.Key)
	uploadID := aliyunoss.ToString(request.UploadId)
	upload, ok := m.multipart[uploadID]
	if !ok || upload.key != key {
		return nil, serviceError(http.StatusNotFound, "NoSuchUpload")
	}
	if request.ForbidOverwrite != nil && *request.ForbidOverwrite == "true" {
		if _, ok := m.objects[key]; ok {
			return nil, serviceError(http.StatusPreconditionFailed, "FileAlreadyExists")
		}
	}
	var data []byte
	if request.CompleteMultipartUpload != nil {
		for _, completed := range request.CompleteMultipartUpload.Parts {
			part, ok := upload.parts[completed.PartNumber]
			if !ok {
				return nil, serviceError(http.StatusNotFound, "NoSuchUpload")
			}
			data = append(data, part.data...)
		}
	}
	acl := string(request.Acl)
	if acl == "" {
		acl = string(aliyunoss.ObjectACLPrivate)
	}
	m.objects[key] = mockObject{
		data:         data,
		contentType:  http.DetectContentType(data),
		lastModified: time.Now().UTC(),
		acl:          acl,
	}
	delete(m.multipart, uploadID)
	return &aliyunoss.CompleteMultipartUploadResult{}, nil
}

func (m *mockClient) AbortMultipartUpload(ctx context.Context, request *aliyunoss.AbortMultipartUploadRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.AbortMultipartUploadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	upload, ok := m.multipart[aliyunoss.ToString(request.UploadId)]
	if !ok || upload.key != aliyunoss.ToString(request.Key) {
		return nil, serviceError(http.StatusNotFound, "NoSuchUpload")
	}
	delete(m.multipart, aliyunoss.ToString(request.UploadId))
	return &aliyunoss.AbortMultipartUploadResult{}, nil
}

func (m *mockClient) GetObject(ctx context.Context, request *aliyunoss.GetObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.GetObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[aliyunoss.ToString(request.Key)]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
	}
	return &aliyunoss.GetObjectResult{
		ContentLength: int64(len(object.data)),
		ContentType:   aliyunoss.Ptr(object.contentType),
		LastModified:  aliyunoss.Ptr(object.lastModified),
		Body:          io.NopCloser(strings.NewReader(string(object.data))),
	}, nil
}

func (m *mockClient) HeadObject(ctx context.Context, request *aliyunoss.HeadObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.HeadObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[aliyunoss.ToString(request.Key)]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
	}
	return &aliyunoss.HeadObjectResult{
		ContentLength: int64(len(object.data)),
		ContentType:   aliyunoss.Ptr(object.contentType),
		LastModified:  aliyunoss.Ptr(object.lastModified),
	}, nil
}

func (m *mockClient) DeleteObject(ctx context.Context, request *aliyunoss.DeleteObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.DeleteObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	delete(m.objects, aliyunoss.ToString(request.Key))
	return &aliyunoss.DeleteObjectResult{}, nil
}

func (m *mockClient) DeleteMultipleObjects(ctx context.Context, request *aliyunoss.DeleteMultipleObjectsRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.DeleteMultipleObjectsResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var objects []aliyunoss.ObjectIdentifier
	if request.Delete != nil {
		objects = request.Delete.Objects
	} else {
		objects = request.Objects
	}
	batch := make([]string, 0, len(objects))
	for _, object := range objects {
		key := aliyunoss.ToString(object.Key)
		batch = append(batch, key)
		delete(m.objects, key)
	}
	m.deleteMultipleBatches = append(m.deleteMultipleBatches, batch)
	return &aliyunoss.DeleteMultipleObjectsResult{}, nil
}

func (m *mockClient) CopyObject(ctx context.Context, request *aliyunoss.CopyObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.CopyObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := m.objects[aliyunoss.ToString(request.SourceKey)]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
	}
	if request.Acl != "" {
		source.acl = string(request.Acl)
	}
	m.objects[aliyunoss.ToString(request.Key)] = source
	return &aliyunoss.CopyObjectResult{}, nil
}

func (m *mockClient) ListObjectsV2(ctx context.Context, request *aliyunoss.ListObjectsV2Request, optFns ...func(*aliyunoss.Options)) (*aliyunoss.ListObjectsV2Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix := aliyunoss.ToString(request.Prefix)
	delimiter := aliyunoss.ToString(request.Delimiter)
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if token := aliyunoss.ToString(request.ContinuationToken); token != "" {
		for i, key := range keys {
			if key > token {
				start = i
				break
			}
			start = len(keys)
		}
	}
	maxKeys := int(request.MaxKeys)
	if maxKeys <= 0 {
		maxKeys = 1000
	}
	result := &aliyunoss.ListObjectsV2Result{}
	commonPrefixes := map[string]struct{}{}
	count := 0
	for _, key := range keys[start:] {
		if count >= maxKeys {
			result.NextContinuationToken = aliyunoss.Ptr(key)
			break
		}
		if delimiter != "" {
			rest := strings.TrimPrefix(key, prefix)
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				commonPrefixes[prefix+rest[:idx+1]] = struct{}{}
				count++
				continue
			}
		}
		object := m.objects[key]
		result.Contents = append(result.Contents, aliyunoss.ObjectProperties{
			Key:          aliyunoss.Ptr(key),
			Size:         int64(len(object.data)),
			LastModified: aliyunoss.Ptr(object.lastModified),
		})
		count++
	}
	for prefix := range commonPrefixes {
		result.CommonPrefixes = append(result.CommonPrefixes, aliyunoss.CommonPrefix{Prefix: aliyunoss.Ptr(prefix)})
	}
	sort.SliceStable(result.CommonPrefixes, func(i, j int) bool {
		return aliyunoss.ToString(result.CommonPrefixes[i].Prefix) < aliyunoss.ToString(result.CommonPrefixes[j].Prefix)
	})
	return result, nil
}

func (m *mockClient) PutObjectAcl(ctx context.Context, request *aliyunoss.PutObjectAclRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.PutObjectAclResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := aliyunoss.ToString(request.Key)
	object, ok := m.objects[key]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
	}
	object.acl = string(request.Acl)
	m.objects[key] = object
	return &aliyunoss.PutObjectAclResult{}, nil
}

func (m *mockClient) GetObjectAcl(ctx context.Context, request *aliyunoss.GetObjectAclRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.GetObjectAclResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[aliyunoss.ToString(request.Key)]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
	}
	return &aliyunoss.GetObjectAclResult{ACL: aliyunoss.Ptr(object.acl)}, nil
}

func (m *mockClient) Presign(ctx context.Context, request any, optFns ...func(*aliyunoss.PresignOptions)) (*aliyunoss.PresignResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	get, ok := request.(*aliyunoss.GetObjectRequest)
	if !ok {
		return nil, errors.New("unsupported presign request")
	}
	values := url.Values{}
	if get.Process != nil {
		values.Set("x-oss-process", aliyunoss.ToString(get.Process))
	}
	rawURL := "https://signed.example.com/" + aliyunoss.ToString(get.Key)
	if encoded := values.Encode(); encoded != "" {
		rawURL += "?" + encoded
	}
	return &aliyunoss.PresignResult{
		Method:     http.MethodGet,
		URL:        rawURL,
		Expiration: time.Now().Add(time.Hour),
	}, nil
}

func serviceError(status int, code string) error {
	return &aliyunoss.ServiceError{StatusCode: status, Code: code}
}
