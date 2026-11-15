package oss

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	aliyunoss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/duolabmeng6/go-filesystem"
)

// A versioned bucket ignores ForbidOverwrite on writes.
type versionedMockClient struct {
	*mockClient
	headErr       error
	putCalls      int
	initCalls     int
	completeCalls int
}

func (m *versionedMockClient) HeadObject(ctx context.Context, request *aliyunoss.HeadObjectRequest, options ...func(*aliyunoss.Options)) (*aliyunoss.HeadObjectResult, error) {
	if m.headErr != nil {
		return nil, m.headErr
	}
	return m.mockClient.HeadObject(ctx, request, options...)
}

func (m *versionedMockClient) PutObject(ctx context.Context, request *aliyunoss.PutObjectRequest, options ...func(*aliyunoss.Options)) (*aliyunoss.PutObjectResult, error) {
	m.putCalls++
	copy := *request
	copy.ForbidOverwrite = nil
	return m.mockClient.PutObject(ctx, &copy, options...)
}

func (m *versionedMockClient) InitiateMultipartUpload(ctx context.Context, request *aliyunoss.InitiateMultipartUploadRequest, options ...func(*aliyunoss.Options)) (*aliyunoss.InitiateMultipartUploadResult, error) {
	m.initCalls++
	copy := *request
	copy.ForbidOverwrite = nil
	return m.mockClient.InitiateMultipartUpload(ctx, &copy, options...)
}

func (m *versionedMockClient) CompleteMultipartUpload(ctx context.Context, request *aliyunoss.CompleteMultipartUploadRequest, options ...func(*aliyunoss.Options)) (*aliyunoss.CompleteMultipartUploadResult, error) {
	m.completeCalls++
	copy := *request
	copy.ForbidOverwrite = nil
	return m.mockClient.CompleteMultipartUpload(ctx, &copy, options...)
}

func TestVersionedBucketChecksExistingObjectsBeforeWrite(t *testing.T) {
	ctx := context.Background()
	client := &versionedMockClient{mockClient: newMockClient()}
	client.objects["existing.txt"] = mockObject{data: []byte("original")}
	client.objects["directory/"] = mockObject{}
	disk, err := NewDisk(Config{Bucket: "bucket", Region: "cn-hangzhou", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"existing.txt", "directory"} {
		t.Run(path, func(t *testing.T) {
			err := disk.Put(ctx, path, []byte("replacement"), filesystem.WithOverwrite(false))
			if !errors.Is(err, filesystem.ErrAlreadyExists) {
				t.Fatalf("write %q: expected existing error, got %v", path, err)
			}
			if client.putCalls != 0 {
				t.Fatal("put request should not be sent for an existing target")
			}
		})
	}
	if string(client.objects["existing.txt"].data) != "original" {
		t.Fatal("existing object was overwritten")
	}
	if err := disk.Put(ctx, "existing.txt", []byte("replacement"), filesystem.WithOverwrite(true)); err != nil {
		t.Fatalf("explicit overwrite: %v", err)
	}
	if string(client.objects["existing.txt"].data) != "replacement" {
		t.Fatal("explicit overwrite did not update the object")
	}
}

func TestVersionedBucketChecksBeforeMultipartStartAndCompletion(t *testing.T) {
	ctx := context.Background()
	client := &versionedMockClient{mockClient: newMockClient()}
	client.objects["existing.txt"] = mockObject{data: []byte("original")}
	disk, err := NewDisk(Config{Bucket: "bucket", Region: "cn-hangzhou", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = disk.CreateMultipartUpload(ctx, "existing.txt", filesystem.WithOverwrite(false))
	if !errors.Is(err, filesystem.ErrAlreadyExists) || client.initCalls != 0 {
		t.Fatalf("start existing upload: err=%v initCalls=%d", err, client.initCalls)
	}
	upload, err := disk.CreateMultipartUpload(ctx, "later.txt", filesystem.WithOverwrite(false))
	if err != nil {
		t.Fatal(err)
	}
	part, err := disk.UploadPart(ctx, "later.txt", upload.UploadID, 1, strings.NewReader("replacement"), -1)
	if err != nil {
		t.Fatal(err)
	}
	client.objects["later.txt"] = mockObject{data: []byte("concurrent existing")}
	err = disk.CompleteMultipartUpload(ctx, "later.txt", upload.UploadID, []filesystem.MultipartUploadPart{part}, filesystem.WithOverwrite(false))
	if !errors.Is(err, filesystem.ErrAlreadyExists) || client.completeCalls != 0 {
		t.Fatalf("complete existing upload: err=%v completeCalls=%d", err, client.completeCalls)
	}
	if string(client.objects["later.txt"].data) != "concurrent existing" {
		t.Fatal("completion overwrote the existing object")
	}
	if err := disk.CompleteMultipartUpload(ctx, "later.txt", upload.UploadID, []filesystem.MultipartUploadPart{part}, filesystem.WithOverwrite(true)); err != nil {
		t.Fatalf("explicit overwrite completion: %v", err)
	}
	if string(client.objects["later.txt"].data) != "replacement" {
		t.Fatal("explicit overwrite did not complete the upload")
	}
}

func TestVersionedBucketPropagatesExistenceCheckError(t *testing.T) {
	ctx := context.Background()
	client := &versionedMockClient{mockClient: newMockClient()}
	disk, err := NewDisk(Config{Bucket: "bucket", Region: "cn-hangzhou", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	checkErr := errors.New("head request failed")
	client.headErr = checkErr
	if err := disk.Write(ctx, "file.txt", strings.NewReader("data"), filesystem.WithOverwrite(false)); !errors.Is(err, checkErr) {
		t.Fatalf("write should return head error: %v", err)
	}
	if _, err := disk.CreateMultipartUpload(ctx, "file.txt", filesystem.WithOverwrite(false)); !errors.Is(err, checkErr) {
		t.Fatalf("start should return head error: %v", err)
	}
	if err := disk.CompleteMultipartUpload(ctx, "file.txt", "id", []filesystem.MultipartUploadPart{{PartNumber: 1, ETag: "tag"}}, filesystem.WithOverwrite(false)); !errors.Is(err, checkErr) {
		t.Fatalf("completion should return head error: %v", err)
	}
	if client.putCalls != 0 || client.initCalls != 0 || client.completeCalls != 0 {
		t.Fatalf("writes occurred after head error: put=%d init=%d complete=%d", client.putCalls, client.initCalls, client.completeCalls)
	}
	if err := disk.Write(ctx, "file.txt", io.NopCloser(strings.NewReader("data")), filesystem.WithOverwrite(true)); err != nil {
		t.Fatalf("explicit overwrite should not check existence: %v", err)
	}
}
