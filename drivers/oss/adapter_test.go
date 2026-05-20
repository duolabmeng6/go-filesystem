package oss

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
)

type mockClient struct {
	objects map[string]mockObject
}

type mockObject struct {
	data         []byte
	contentType  string
	lastModified time.Time
	acl          string
}

func newMockClient() *mockClient {
	return &mockClient{objects: map[string]mockObject{}}
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

func (m *mockClient) CopyObject(ctx context.Context, request *aliyunoss.CopyObjectRequest, optFns ...func(*aliyunoss.Options)) (*aliyunoss.CopyObjectResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, ok := m.objects[aliyunoss.ToString(request.SourceKey)]
	if !ok {
		return nil, serviceError(http.StatusNotFound, "NoSuchKey")
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
	return &aliyunoss.PresignResult{
		Method:     http.MethodGet,
		URL:        "https://signed.example.com/" + aliyunoss.ToString(get.Key),
		Expiration: time.Now().Add(time.Hour),
	}, nil
}

func serviceError(status int, code string) error {
	return &aliyunoss.ServiceError{StatusCode: status, Code: code}
}
