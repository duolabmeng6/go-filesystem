package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/duolabmeng6/go-filesystem"
)

// The preflight responses deliberately remain stale to model concurrent clients.
func TestConditionalWritesPreventConcurrentOverwrite(t *testing.T) {
	for _, mode := range []string{"put", "complete multipart"} {
		t.Run(mode, func(t *testing.T) {
			var mu sync.Mutex
			var stored string
			var writes int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodHead {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				writes++
				if r.Header.Get("If-None-Match") != "*" {
					t.Errorf("missing If-None-Match: %q", r.Header.Get("If-None-Match"))
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if stored != "" {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(http.StatusPreconditionFailed)
					_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
					return
				}
				stored = string(data)
				if mode == "complete multipart" {
					w.Header().Set("Content-Type", "application/xml")
					_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><ETag>done</ETag></CompleteMultipartUploadResult>")
				}
			}))
			defer server.Close()
			client := awss3.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test-secret", ""),
				RetryMaxAttempts: 1,
			}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
			adapter, err := New(context.Background(), Config{Bucket: "bucket", Region: "us-east-1", DisableACL: true, Client: client})
			if err != nil {
				t.Fatal(err)
			}
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, value := range []string{"first", "second"} {
				go func() {
					<-start
					if mode == "put" {
						results <- adapter.Write(context.Background(), "doc.txt", strings.NewReader(value), filesystem.WriteOptions{})
						return
					}
					parts := []filesystem.MultipartUploadPart{{PartNumber: 1, ETag: value}}
					results <- adapter.CompleteMultipartUpload(context.Background(), "doc.txt", value, parts, filesystem.WriteOptions{})
				}()
			}
			close(start)
			success, exists := 0, 0
			for range 2 {
				err := <-results
				switch {
				case err == nil:
					success++
				case errors.Is(err, filesystem.ErrAlreadyExists):
					exists++
				default:
					t.Fatalf("unexpected write result: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if success != 1 || exists != 1 || writes != 2 || stored == "" {
				t.Fatalf("success=%d exists=%d writes=%d stored=%q", success, exists, writes, stored)
			}
		})
	}
}

type failingConditionalClient struct {
	*mockClient
	code      string
	puts      int
	completes int
}

func (c *failingConditionalClient) PutObject(_ context.Context, _ *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	c.puts++
	return nil, apiError(c.code)
}

func (c *failingConditionalClient) CompleteMultipartUpload(_ context.Context, _ *awss3.CompleteMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	c.completes++
	return nil, apiError(c.code)
}

func TestConditionalWriteErrorsDoNotFallBack(t *testing.T) {
	client := &failingConditionalClient{mockClient: newMockClient(), code: "NotImplemented"}
	adapter, err := New(context.Background(), Config{Bucket: "bucket", Region: "us-east-1", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Write(context.Background(), "doc", strings.NewReader("content"), filesystem.WriteOptions{})
	if err == nil || errors.Is(err, filesystem.ErrAlreadyExists) || client.puts != 1 {
		t.Fatalf("expected unsupported conditional write to fail without retry, got %v after %d puts", err, client.puts)
	}
	parts := []filesystem.MultipartUploadPart{{PartNumber: 1, ETag: "test"}}
	err = adapter.CompleteMultipartUpload(context.Background(), "doc", "upload", parts, filesystem.WriteOptions{})
	if err == nil || errors.Is(err, filesystem.ErrAlreadyExists) || client.completes != 1 {
		t.Fatalf("expected unsupported conditional completion to fail without retry, got %v after %d completions", err, client.completes)
	}
	client.code = "ConditionalRequestConflict"
	err = adapter.CompleteMultipartUpload(context.Background(), "doc", "upload", parts, filesystem.WriteOptions{})
	if !errors.Is(err, filesystem.ErrMultipartConflict) || errors.Is(err, filesystem.ErrAlreadyExists) || !strings.Contains(err.Error(), "create a new upload") {
		t.Fatalf("expected conflict requiring a new upload, got %v", err)
	}
	var conflict interface{ ErrorCode() string }
	if !errors.As(err, &conflict) || conflict.ErrorCode() != "ConditionalRequestConflict" {
		t.Fatalf("expected original service error to remain inspectable, got %v", err)
	}
	err = adapter.Write(context.Background(), "doc", strings.NewReader("content"), filesystem.WriteOptions{})
	if err == nil || errors.Is(err, filesystem.ErrAlreadyExists) || errors.Is(err, filesystem.ErrMultipartConflict) {
		t.Fatalf("ordinary put conflict must not be reported as existing or multipart conflict: %v", err)
	}
	client.code = "PreconditionFailed"
	err = adapter.Write(context.Background(), "doc", strings.NewReader("content"), filesystem.WriteOptions{})
	if !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("ordinary put 412 should report existing file: %v", err)
	}
	err = adapter.CompleteMultipartUpload(context.Background(), "doc", "upload", parts, filesystem.WriteOptions{})
	if !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("multipart completion 412 should report existing file: %v", err)
	}
}

func TestOverwriteWritesDoNotSendConditionalHeader(t *testing.T) {
	client := newMockClient()
	client.objects["doc"] = mockObject{data: []byte("old")}
	adapter, err := New(context.Background(), Config{Bucket: "bucket", Region: "us-east-1", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	options := filesystem.WriteOptions{Overwrite: true}
	if err := adapter.Write(ctx, "doc", strings.NewReader("new"), options); err != nil {
		t.Fatal(err)
	}
	if client.lastPut.IfNoneMatch != nil || string(client.objects["doc"].data) != "new" {
		t.Fatalf("overwrite put used condition or failed: %#v", client.lastPut.IfNoneMatch)
	}
	upload, err := adapter.CreateMultipartUpload(ctx, "doc", options)
	if err != nil {
		t.Fatal(err)
	}
	part, err := adapter.UploadPart(ctx, "doc", upload.UploadID, 1, strings.NewReader("multipart"), 9)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CompleteMultipartUpload(ctx, "doc", upload.UploadID, []filesystem.MultipartUploadPart{part}, options); err != nil {
		t.Fatal(err)
	}
	if client.lastComplete.IfNoneMatch != nil || string(client.objects["doc"].data) != "multipart" {
		t.Fatalf("overwrite completion used condition or failed: %#v", client.lastComplete.IfNoneMatch)
	}
}
