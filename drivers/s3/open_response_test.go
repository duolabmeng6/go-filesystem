package s3

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

type openResponseTransport struct {
	base   http.RoundTripper
	closed atomic.Int32
}

type openResponseBody struct {
	io.ReadCloser
	closed *atomic.Int32
}

func (b *openResponseBody) Close() error {
	b.closed.Add(1)
	return b.ReadCloser.Close()
}

func (t *openResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(req)
	if err == nil {
		res.Body = &openResponseBody{ReadCloser: res.Body, closed: &t.closed}
	}
	return res, err
}

func TestOpenSDKRejectsIncompleteResponses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		contentRange string
		body         string
		wantError    bool
	}{
		{"complete", 200, "", "contents", false},
		{"empty", 200, "", "", false},
		{"non-authoritative", 203, "", "contents", false},
		{"partial with range", 206, "bytes 0-3/8", "part", true},
		{"partial without range", 206, "", "part", true},
		{"range with ok", 200, "bytes 0-3/8", "part", true},
		{"accepted", 202, "", "part", true},
		{"no content", 204, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					t.Errorf("unexpected Range request")
				}
				if tc.contentRange != "" {
					w.Header().Set("Content-Range", tc.contentRange)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			transport := &openResponseTransport{base: http.DefaultTransport}
			sdk := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""), HTTPClient: &http.Client{Transport: transport}}, func(o *awss3.Options) {
				o.BaseEndpoint = aws.String(server.URL)
				o.UsePathStyle = true
				applyS3CompatibilityOptions(o)
			})
			a := &Adapter{client: sdk, bucket: "test"}
			body, err := a.Open(context.Background(), "file")
			if tc.wantError {
				if err == nil || body != nil {
					t.Fatalf("expected error, got body %v, err %v", body, err)
				}
				if transport.closed.Load() != 1 {
					t.Fatalf("response body was not closed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(body)
			if err != nil {
				t.Fatal(err)
			}
			if err = body.Close(); err != nil {
				t.Fatal(err)
			}
			if string(data) != tc.body {
				t.Fatalf("got %q, want %q", data, tc.body)
			}
		})
	}
}

type openResponseClient struct {
	*mockClient
	output *awss3.GetObjectOutput
	err    error
}

func (c *openResponseClient) GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error) {
	return c.output, c.err
}

func TestOpenClosesBodyOnClientError(t *testing.T) {
	var closed atomic.Int32
	body := &openResponseBody{ReadCloser: io.NopCloser(strings.NewReader("partial")), closed: &closed}
	want := errors.New("upstream")
	a := &Adapter{client: &openResponseClient{output: &awss3.GetObjectOutput{Body: body}, err: want}}
	result, err := a.Open(context.Background(), "file")
	if result != nil || !errors.Is(err, want) || closed.Load() != 1 {
		t.Fatalf("body %v, error %v, closes %d", result, err, closed.Load())
	}
}

func TestOpenRejectsMissingBody(t *testing.T) {
	for _, length := range []*int64{nil, aws.Int64(0), aws.Int64(42)} {
		a := &Adapter{client: &openResponseClient{output: &awss3.GetObjectOutput{ContentLength: length}}}
		body, err := a.Open(context.Background(), "file")
		if err == nil || body != nil {
			t.Fatalf("length %v: expected error, got body %v, err %v", length, body, err)
		}
	}
}
