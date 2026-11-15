package webdav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
)

func TestFullReadRejectsPartialResponse(t *testing.T) {
	for _, tc := range []struct {
		name, contentRange string
		status             int
		wantError          bool
	}{
		{"complete", "", http.StatusOK, false},
		{"complete non authoritative", "", http.StatusNonAuthoritativeInfo, false},
		{"non authoritative with range", "bytes 0-2/10", http.StatusNonAuthoritativeInfo, true},
		{"partial", "bytes 0-2/10", http.StatusPartialContent, true},
		{"partial without header", "", http.StatusPartialContent, true},
		{"range header on ok", "bytes 0-2/10", http.StatusOK, true},
		{"accepted", "", http.StatusAccepted, true},
		{"no content", "", http.StatusNoContent, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
					t.Errorf("unexpected request: %s Range=%q", r.Method, r.Header.Get("Range"))
				}
				if tc.contentRange != "" {
					w.Header().Set("Content-Range", tc.contentRange)
				}
				w.WriteHeader(tc.status)
				if tc.status != http.StatusNoContent {
					_, _ = io.WriteString(w, "abc")
				}
			}))
			defer server.Close()
			a, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := a.Open(context.Background(), "test.txt")
			if tc.wantError {
				if err == nil {
					reader.Close()
					t.Fatal("partial or incomplete response accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(reader)
			reader.Close()
			if err != nil || string(body) != "abc" {
				t.Fatalf("body = %q, %v", body, err)
			}
		})
	}
}

func TestWriteRequiresCompletedResponse(t *testing.T) {
	for _, status := range []int{200, 201, 202, 203, 204, 206, 207} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut {
					t.Errorf("unexpected method %s", r.Method)
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.WriteHeader(status)
			}))
			defer server.Close()
			a, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
			if err != nil {
				t.Fatal(err)
			}
			err = a.Write(context.Background(), "test.txt", strings.NewReader("data"), filesystem.DefaultWriteOptions())
			wantError := status != 200 && status != 201 && status != 203 && status != 204
			if (err != nil) != wantError {
				t.Fatalf("status %d: error = %v, wantError %v", status, err, wantError)
			}
		})
	}
}

func TestPropfindMergesSuccessfulProperties(t *testing.T) {
	for _, tc := range []struct {
		name, props string
		wantError   bool
		wantDir     bool
		wantSize    int64
	}{
		{"split directory", `<propstat><prop><getcontentlength>3</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat><propstat><prop><resourcetype><collection/></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat>`, false, true, 3},
		{"empty file type", `<propstat><prop><resourcetype/><getcontentlength>0</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat>`, false, false, 0},
		{"ignore failed type", `<propstat><prop><resourcetype><collection/></resourcetype></prop><status>HTTP/1.1 404 Not Found</status></propstat><propstat><prop><resourcetype/></prop><status>HTTP/1.1 200 OK</status></propstat>`, false, false, 0},
		{"ignore invalid failed size", `<propstat><prop><getcontentlength>unavailable</getcontentlength></prop><status>HTTP/1.1 404 Not Found</status></propstat><propstat><prop><resourcetype/><getcontentlength>0</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat>`, false, false, 0},
		{"accepted type not confirmed", `<propstat><prop><resourcetype/></prop><status>HTTP/1.1 202 Accepted</status></propstat>`, true, false, 0},
		{"partial type not confirmed", `<propstat><prop><resourcetype/></prop><status>HTTP/1.1 206 Partial Content</status></propstat>`, true, false, 0},
		{"reject unconfirmed values", `<propstat><prop><resourcetype><collection/></resourcetype><getcontentlength>9</getcontentlength></prop><status>HTTP/1.1 202 Accepted</status></propstat><propstat><prop><resourcetype/><getcontentlength>0</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat>`, true, false, 0},
		{"reject partial size", `<propstat><prop><resourcetype/></prop><status>HTTP/1.1 200 OK</status></propstat><propstat><prop><getcontentlength>9</getcontentlength></prop><status>HTTP/1.1 206 Partial Content</status></propstat>`, true, false, 0},
		{"non authoritative properties", `<propstat><prop><resourcetype/><getcontentlength>2</getcontentlength></prop><status>HTTP/1.1 203 Non-Authoritative Information</status></propstat>`, false, false, 2},
		{"missing type", `<propstat><prop><getcontentlength>3</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat>`, true, false, 0},
		{"conflicting type", `<propstat><prop><resourcetype/></prop><status>HTTP/1.1 200 OK</status></propstat><propstat><prop><resourcetype><collection/></resourcetype></prop><status>HTTP/1.1 200 OK</status></propstat>`, true, false, 0},
		{"negative size", `<propstat><prop><resourcetype/><getcontentlength>-1</getcontentlength></prop><status>HTTP/1.1 200 OK</status></propstat>`, true, false, 0},
		{"invalid status", `<propstat><prop><resourcetype/></prop><status>invalid</status></propstat>`, true, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusMultiStatus)
				_, _ = io.WriteString(w, `<multistatus xmlns="DAV:"><response><href>/dav/item</href>`+tc.props+`</response></multistatus>`)
			}))
			defer server.Close()
			a, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
			if err != nil {
				t.Fatal(err)
			}
			info, err := a.Stat(context.Background(), "item")
			if (err != nil) != tc.wantError {
				t.Fatalf("info = %+v, error = %v, wantError %v", info, err, tc.wantError)
			}
			if !tc.wantError && (info.IsDir != tc.wantDir || info.Size != tc.wantSize) {
				t.Fatalf("info = %+v, want directory=%v size=%d", info, tc.wantDir, tc.wantSize)
			}
		})
	}
}

func TestMutationMultistatusChecksEachResponse(t *testing.T) {
	for _, method := range []string{http.MethodDelete, "COPY", "MOVE"} {
		for _, responseStatus := range []string{"HTTP/1.1 204 No Content", "HTTP/1.1 403 Forbidden", "HTTP/1.1 202 Accepted", "HTTP/1.1 2xx Invalid"} {
			t.Run(method+"/"+responseStatus, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusMultiStatus)
					_, _ = io.WriteString(w, `<multistatus xmlns="DAV:"><response><href>/dav/item</href><status>`+responseStatus+`</status></response></multistatus>`)
				}))
				defer server.Close()
				a, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
				if err != nil {
					t.Fatal(err)
				}
				res, err := a.(*Adapter).request(context.Background(), method, "item", nil, nil)
				if err == nil {
					res.Body.Close()
				}
				if (err != nil) != !strings.Contains(responseStatus, "204") {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}

func TestMutationRejectsAsynchronousAndPartialResponses(t *testing.T) {
	for _, method := range []string{http.MethodPut, "MKCOL", http.MethodDelete, "COPY", "MOVE"} {
		for _, status := range []int{http.StatusAccepted, http.StatusPartialContent, http.StatusNonAuthoritativeInfo, http.StatusNoContent} {
			t.Run(fmt.Sprintf("%s/%d", method, status), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
				}))
				defer server.Close()
				a, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
				if err != nil {
					t.Fatal(err)
				}
				res, err := a.(*Adapter).request(context.Background(), method, "item", nil, nil)
				if err == nil {
					res.Body.Close()
				}
				if (err != nil) != (status != http.StatusNoContent && status != http.StatusNonAuthoritativeInfo) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
}
