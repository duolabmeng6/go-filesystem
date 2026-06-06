package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
)

type mockClient struct {
	objects      map[string]mockObject
	multipart    map[string]mockMultipartUpload
	lastPut      *awss3.PutObjectInput
	lastComplete *awss3.CompleteMultipartUploadInput
}

type mockObject struct {
	data         []byte
	contentType  string
	lastModified time.Time
	acl          types.ObjectCannedACL
}

type mockMultipartUpload struct {
	key   string
	acl   types.ObjectCannedACL
	parts map[int32]mockMultipartPart
}

type mockMultipartPart struct {
	data []byte
	etag string
	size int64
}

type mockPresigner struct{}

func newMockClient() *mockClient {
	return &mockClient{
		objects:   map[string]mockObject{},
		multipart: map[string]mockMultipartUpload{},
	}
}

func newTestDisk(t testing.TB) *filesystem.Disk {
	t.Helper()
	disk, err := NewDisk(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		BaseURL:       "https://cdn.example.com/files",
		Visibility:    filesystem.VisibilityPrivate,
		Client:        newMockClient(),
		PresignClient: mockPresigner{},
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
		disk, err := NewDisk(context.Background(), Config{
			Bucket:        "bucket",
			Region:        "us-east-1",
			BaseURL:       "/storage",
			Client:        newMockClient(),
			PresignClient: mockPresigner{},
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

func TestTemporaryURLResponseParameters(t *testing.T) {
	presigner := &recordingPresigner{}
	disk, err := NewDisk(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		Client:        newMockClient(),
		PresignClient: presigner,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	_, err = disk.TemporaryURL(
		context.Background(),
		"a.txt",
		time.Now().Add(time.Hour),
		filesystem.WithURLParameter("response-content-disposition", `attachment; filename="a.txt"`),
	)
	if err != nil {
		t.Fatalf("temporary url: %v", err)
	}
	if presigner.input == nil || presigner.input.ResponseContentDisposition == nil || *presigner.input.ResponseContentDisposition != `attachment; filename="a.txt"` {
		t.Fatalf("expected response-content-disposition to be forwarded, got %#v", presigner.input)
	}
}

func TestFactoryUsesOptions(t *testing.T) {
	manager := filesystem.New(filesystem.WithDriver("s3", NewFactory()))
	disk, err := manager.Build(context.Background(), filesystem.DiskConfig{
		Driver:  "s3",
		BaseURL: "https://cdn.example.com",
		Options: map[string]any{
			"bucket":            "bucket",
			"region":            "us-east-1",
			"access_key_id":     "ak",
			"access_key_secret": "sk",
			"use_path_style":    true,
			"disable_ssl":       true,
			"disable_acl":       true,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if disk == nil {
		t.Fatalf("expected disk")
	}
}

func TestDisableACL(t *testing.T) {
	client := newMockClient()
	disk, err := NewDisk(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		Client:        client,
		PresignClient: mockPresigner{},
		DisableACL:    true,
	})
	if err != nil {
		t.Fatalf("new disk: %v", err)
	}
	if disk.Adapter().Capabilities().Has(filesystem.CapabilityVisibility) {
		t.Fatalf("visibility capability should be disabled")
	}
	if err := disk.Put(context.Background(), "private.txt", []byte("x")); err != nil {
		t.Fatalf("put without visibility: %v", err)
	}
	if err := disk.Put(context.Background(), "public.txt", []byte("x"), filesystem.WithVisibility(filesystem.VisibilityPublic)); !errors.Is(err, filesystem.ErrUnsupported) {
		t.Fatalf("expected unsupported visibility, got %v", err)
	}
	if err := disk.SetVisibility(context.Background(), "private.txt", filesystem.VisibilityPublic); !errors.Is(err, filesystem.ErrUnsupported) {
		t.Fatalf("expected unsupported set visibility, got %v", err)
	}
}

func TestCompatibilityOptionsUseRequiredChecksums(t *testing.T) {
	options := awss3.Options{}
	applyS3CompatibilityOptions(&options)

	if options.RequestChecksumCalculation != aws.RequestChecksumCalculationWhenRequired {
		t.Fatalf("expected request checksum when required, got %#v", options.RequestChecksumCalculation)
	}
	if options.ResponseChecksumValidation != aws.ResponseChecksumValidationWhenRequired {
		t.Fatalf("expected response checksum when required, got %#v", options.ResponseChecksumValidation)
	}
}

func TestWriteWithoutOverwriteUsesPreflightInsteadOfConditionalPut(t *testing.T) {
	client := newMockClient()
	adapter, err := New(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		Client:        client,
		PresignClient: mockPresigner{},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	if err := adapter.Write(context.Background(), "a.txt", strings.NewReader("alpha"), filesystem.WriteOptions{}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if client.lastPut == nil {
		t.Fatalf("expected put object call")
	}
	if client.lastPut.IfNoneMatch != nil {
		t.Fatalf("S3-compatible writes should not send IfNoneMatch, got %q", *client.lastPut.IfNoneMatch)
	}
	if err := adapter.Write(context.Background(), "a.txt", strings.NewReader("bravo"), filesystem.WriteOptions{}); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("expected preflight ErrAlreadyExists, got %v", err)
	}
}

func TestWriteSpoolsReaderToProvideContentLength(t *testing.T) {
	client := newMockClient()
	adapter, err := New(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		Client:        client,
		PresignClient: mockPresigner{},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	reader := io.LimitReader(strings.NewReader("streamed"), int64(len("streamed")))
	if err := adapter.Write(context.Background(), "streamed.txt", reader, filesystem.WriteOptions{Overwrite: true}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if client.lastPut == nil || client.lastPut.ContentLength == nil {
		t.Fatalf("expected put object content length")
	}
	if *client.lastPut.ContentLength != int64(len("streamed")) {
		t.Fatalf("expected content length %d, got %d", len("streamed"), *client.lastPut.ContentLength)
	}
	if got := string(client.objects["streamed.txt"].data); got != "streamed" {
		t.Fatalf("unexpected stored content %q", got)
	}
}

func TestMultipartCompleteDoesNotSendConditionalHeader(t *testing.T) {
	client := newMockClient()
	adapter, err := New(context.Background(), Config{
		Bucket:        "bucket",
		Region:        "us-east-1",
		Client:        client,
		PresignClient: mockPresigner{},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	upload, err := adapter.CreateMultipartUpload(context.Background(), "archive.zip", filesystem.WriteOptions{})
	if err != nil {
		t.Fatalf("create multipart: %v", err)
	}
	part, err := adapter.UploadPart(context.Background(), "archive.zip", upload.UploadID, 1, strings.NewReader("zip"), 3)
	if err != nil {
		t.Fatalf("upload part: %v", err)
	}
	if err := adapter.CompleteMultipartUpload(context.Background(), "archive.zip", upload.UploadID, []filesystem.MultipartUploadPart{part}, filesystem.WriteOptions{}); err != nil {
		t.Fatalf("complete multipart: %v", err)
	}
	if client.lastComplete == nil {
		t.Fatalf("expected complete multipart call")
	}
	if client.lastComplete.IfNoneMatch != nil {
		t.Fatalf("S3-compatible multipart complete should not send IfNoneMatch, got %q", *client.lastComplete.IfNoneMatch)
	}
}

func (m *mockClient) PutObject(ctx context.Context, input *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.lastPut = input
	key := awsString(input.Key)
	if input.IfNoneMatch != nil && *input.IfNoneMatch == "*" {
		if _, ok := m.objects[key]; ok {
			return nil, apiError("PreconditionFailed")
		}
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	acl := input.ACL
	if acl == "" {
		acl = types.ObjectCannedACLPrivate
	}
	m.objects[key] = mockObject{data: data, contentType: http.DetectContentType(data), lastModified: time.Now().UTC(), acl: acl}
	return &awss3.PutObjectOutput{}, nil
}

func (m *mockClient) CreateMultipartUpload(ctx context.Context, input *awss3.CreateMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	acl := input.ACL
	if acl == "" {
		acl = types.ObjectCannedACLPrivate
	}
	uploadID := "upload-" + string(rune('a'+len(m.multipart)))
	m.multipart[uploadID] = mockMultipartUpload{
		key:   awsString(input.Key),
		acl:   acl,
		parts: map[int32]mockMultipartPart{},
	}
	return &awss3.CreateMultipartUploadOutput{UploadId: stringPtr(uploadID)}, nil
}

func (m *mockClient) UploadPart(ctx context.Context, input *awss3.UploadPartInput, optFns ...func(*awss3.Options)) (*awss3.UploadPartOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	uploadID := awsString(input.UploadId)
	upload, ok := m.multipart[uploadID]
	if !ok || upload.key != awsString(input.Key) {
		return nil, apiError("NoSuchUpload")
	}
	data, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	partNumber := int32(0)
	if input.PartNumber != nil {
		partNumber = *input.PartNumber
	}
	etag := "etag-" + string(rune('a'+partNumber))
	upload.parts[partNumber] = mockMultipartPart{data: data, etag: etag, size: int64(len(data))}
	m.multipart[uploadID] = upload
	return &awss3.UploadPartOutput{ETag: stringPtr(etag)}, nil
}

func (m *mockClient) ListParts(ctx context.Context, input *awss3.ListPartsInput, optFns ...func(*awss3.Options)) (*awss3.ListPartsOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	upload, ok := m.multipart[awsString(input.UploadId)]
	if !ok || upload.key != awsString(input.Key) {
		return nil, apiError("NoSuchUpload")
	}
	numbers := make([]int, 0, len(upload.parts))
	for number := range upload.parts {
		numbers = append(numbers, int(number))
	}
	sort.Ints(numbers)
	output := &awss3.ListPartsOutput{IsTruncated: boolPtr(false)}
	for _, number := range numbers {
		part := upload.parts[int32(number)]
		partNumber := int32(number)
		size := part.size
		output.Parts = append(output.Parts, types.Part{
			PartNumber: &partNumber,
			ETag:       stringPtr(part.etag),
			Size:       &size,
		})
	}
	return output, nil
}

func (m *mockClient) CompleteMultipartUpload(ctx context.Context, input *awss3.CompleteMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.lastComplete = input
	key := awsString(input.Key)
	uploadID := awsString(input.UploadId)
	upload, ok := m.multipart[uploadID]
	if !ok || upload.key != key {
		return nil, apiError("NoSuchUpload")
	}
	if input.IfNoneMatch != nil && *input.IfNoneMatch == "*" {
		if _, ok := m.objects[key]; ok {
			return nil, apiError("PreconditionFailed")
		}
	}
	var data []byte
	if input.MultipartUpload != nil {
		for _, completed := range input.MultipartUpload.Parts {
			if completed.PartNumber == nil {
				return nil, apiError("NoSuchUpload")
			}
			part, ok := upload.parts[*completed.PartNumber]
			if !ok {
				return nil, apiError("NoSuchUpload")
			}
			data = append(data, part.data...)
		}
	}
	acl := upload.acl
	if acl == "" {
		acl = types.ObjectCannedACLPrivate
	}
	m.objects[key] = mockObject{data: data, contentType: http.DetectContentType(data), lastModified: time.Now().UTC(), acl: acl}
	delete(m.multipart, uploadID)
	return &awss3.CompleteMultipartUploadOutput{}, nil
}

func (m *mockClient) AbortMultipartUpload(ctx context.Context, input *awss3.AbortMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	upload, ok := m.multipart[awsString(input.UploadId)]
	if !ok || upload.key != awsString(input.Key) {
		return nil, apiError("NoSuchUpload")
	}
	delete(m.multipart, awsString(input.UploadId))
	return &awss3.AbortMultipartUploadOutput{}, nil
}

func (m *mockClient) GetObject(ctx context.Context, input *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[awsString(input.Key)]
	if !ok {
		return nil, apiError("NoSuchKey")
	}
	return &awss3.GetObjectOutput{
		Body:          io.NopCloser(strings.NewReader(string(object.data))),
		ContentLength: int64Ptr(int64(len(object.data))),
		ContentType:   stringPtr(object.contentType),
		LastModified:  &object.lastModified,
	}, nil
}

func (m *mockClient) HeadObject(ctx context.Context, input *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[awsString(input.Key)]
	if !ok {
		return nil, apiError("NotFound")
	}
	return &awss3.HeadObjectOutput{
		ContentLength: int64Ptr(int64(len(object.data))),
		ContentType:   stringPtr(object.contentType),
		LastModified:  &object.lastModified,
	}, nil
}

func (m *mockClient) DeleteObject(ctx context.Context, input *awss3.DeleteObjectInput, optFns ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	delete(m.objects, awsString(input.Key))
	return &awss3.DeleteObjectOutput{}, nil
}

func (m *mockClient) CopyObject(ctx context.Context, input *awss3.CopyObjectInput, optFns ...func(*awss3.Options)) (*awss3.CopyObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source := awsString(input.CopySource)
	source = strings.TrimPrefix(source, awsString(input.Bucket)+"/")
	source, _ = urlPathUnescape(source)
	object, ok := m.objects[source]
	if !ok {
		return nil, apiError("NoSuchKey")
	}
	m.objects[awsString(input.Key)] = object
	return &awss3.CopyObjectOutput{}, nil
}

func (m *mockClient) ListObjectsV2(ctx context.Context, input *awss3.ListObjectsV2Input, optFns ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix := awsString(input.Prefix)
	delimiter := awsString(input.Delimiter)
	keys := make([]string, 0, len(m.objects))
	for key := range m.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	start := 0
	if token := awsString(input.ContinuationToken); token != "" {
		for i, key := range keys {
			if key > token {
				start = i
				break
			}
			start = len(keys)
		}
	}
	maxKeys := 1000
	if input.MaxKeys != nil {
		maxKeys = int(*input.MaxKeys)
	}
	output := &awss3.ListObjectsV2Output{}
	commonPrefixes := map[string]struct{}{}
	count := 0
	for _, key := range keys[start:] {
		if count >= maxKeys {
			output.NextContinuationToken = stringPtr(key)
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
		output.Contents = append(output.Contents, types.Object{Key: stringPtr(key), Size: int64Ptr(int64(len(object.data))), LastModified: &object.lastModified})
		count++
	}
	for prefix := range commonPrefixes {
		output.CommonPrefixes = append(output.CommonPrefixes, types.CommonPrefix{Prefix: stringPtr(prefix)})
	}
	sort.SliceStable(output.CommonPrefixes, func(i, j int) bool {
		return awsString(output.CommonPrefixes[i].Prefix) < awsString(output.CommonPrefixes[j].Prefix)
	})
	return output, nil
}

func (m *mockClient) PutObjectAcl(ctx context.Context, input *awss3.PutObjectAclInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectAclOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := awsString(input.Key)
	object, ok := m.objects[key]
	if !ok {
		return nil, apiError("NoSuchKey")
	}
	object.acl = input.ACL
	m.objects[key] = object
	return &awss3.PutObjectAclOutput{}, nil
}

func (m *mockClient) GetObjectAcl(ctx context.Context, input *awss3.GetObjectAclInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectAclOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	object, ok := m.objects[awsString(input.Key)]
	if !ok {
		return nil, apiError("NoSuchKey")
	}
	output := &awss3.GetObjectAclOutput{}
	if object.acl == types.ObjectCannedACLPublicRead || object.acl == types.ObjectCannedACLPublicReadWrite {
		output.Grants = append(output.Grants, types.Grant{
			Permission: types.PermissionRead,
			Grantee: &types.Grantee{
				Type: types.TypeGroup,
				URI:  stringPtr("http://acs.amazonaws.com/groups/global/AllUsers"),
			},
		})
	}
	return output, nil
}

func (mockPresigner) PresignGetObject(ctx context.Context, input *awss3.GetObjectInput, optFns ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &v4.PresignedHTTPRequest{URL: "https://signed.example.com/" + awsString(input.Key) + "?X-Amz-Signature=1"}, nil
}

type recordingPresigner struct {
	input *awss3.GetObjectInput
}

func (r *recordingPresigner) PresignGetObject(ctx context.Context, input *awss3.GetObjectInput, optFns ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.input = input
	return &v4.PresignedHTTPRequest{URL: "https://signed.example.com/" + awsString(input.Key) + "?X-Amz-Signature=1"}, nil
}

func apiError(code string) error {
	return &smithy.GenericAPIError{Code: code, Message: code, Fault: smithy.FaultClient}
}

func int64Ptr(value int64) *int64 { return &value }

func boolPtr(value bool) *bool { return &value }

func urlPathUnescape(value string) (string, error) {
	return strings.ReplaceAll(value, "%20", " "), nil
}
