package webdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	xwebdav "golang.org/x/net/webdav"
)

func TestAdapterFileOperations(t *testing.T) {
	handler := &xwebdav.Handler{Prefix: "/dav", FileSystem: xwebdav.NewMemFS(), LockSystem: xwebdav.NewMemLS()}
	// Some providers reject MOVE when the destination exists; overwrites must use PUT.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "MOVE" && strings.Contains(r.URL.Path, ".afile-") {
			http.Error(w, "overwrite unsupported", http.StatusConflict)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	adapter, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/", "username": "user", "password": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dirs := adapter.(filesystem.DirectoryManager)
	cp := adapter.(filesystem.Copier)
	mv := adapter.(filesystem.Mover)
	if err = dirs.MakeDirectory(ctx, "文件夹", filesystem.DirectoryOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = adapter.Write(ctx, "文件夹/测试.txt", strings.NewReader("first"), filesystem.DefaultWriteOptions()); err != nil {
		t.Fatal(err)
	}
	if err = adapter.Write(ctx, "文件夹/测试.txt", strings.NewReader("second"), filesystem.DefaultWriteOptions()); err != nil {
		t.Fatal(err)
	}
	read, err := adapter.Open(ctx, "文件夹/测试.txt")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(read)
	read.Close()
	if err != nil || string(contents) != "second" {
		t.Fatalf("read = %q, %v", contents, err)
	}
	if err = cp.Copy(ctx, "文件夹/测试.txt", "文件夹/copy.txt"); err != nil {
		t.Fatal(err)
	}
	if err = mv.Move(ctx, "文件夹/copy.txt", "文件夹/moved.txt"); err != nil {
		t.Fatal(err)
	}
	page, err := adapter.ListPage(ctx, "文件夹", filesystem.ListOptions{PageSize: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := adapter.ListPage(ctx, "文件夹", filesystem.ListOptions{PageSize: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].Path == page.Items[0].Path {
		t.Fatalf("next page = %+v, %v", next, err)
	}
	if err = adapter.Write(ctx, "文件夹/测试.txt", strings.NewReader("overwrite"), filesystem.WriteOptions{}); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("exclusive write = %v", err)
	}
	if err = adapter.Delete(ctx, "文件夹/moved.txt"); err != nil {
		t.Fatal(err)
	}
	if err = dirs.DeleteDirectory(ctx, "文件夹"); err != nil {
		t.Fatal(err)
	}
	if exists, err := adapter.Exists(ctx, "文件夹"); err != nil || exists {
		t.Fatalf("exists = %v, %v", exists, err)
	}
	if _, err = adapter.Stat(ctx, "../escape"); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("escape = %v", err)
	}
}

func TestRejectsInvalidAddressAndPartialFailure(t *testing.T) {
	for _, address := range []string{"https://user:pass@example.test/dav", "https://example.test/dav/../other", "https://example.test/dav?token=x"} {
		if _, err := ParseURL(address); err == nil {
			t.Errorf("accepted invalid address %q", address)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `<multistatus xmlns="DAV:"><response><href>/dav/file</href><status>HTTP/1.1 403 Forbidden</status></response></multistatus>`)
	}))
	defer server.Close()
	adapter, err := NewFactory()(context.Background(), filesystem.DiskConfig{Options: map[string]any{"url": server.URL + "/dav/"}})
	if err != nil {
		t.Fatal(err)
	}
	// Verify direct operation responses; some servers return 207 even when part of a tree failed.
	_, err = adapter.(*Adapter).request(context.Background(), "DELETE", "file", nil, nil)
	if err == nil {
		t.Fatal("partial failure was reported as success")
	}
}
