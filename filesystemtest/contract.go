package filesystemtest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

func RunObjectContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("write read exists delete", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		if err := disk.Put(ctx, "docs/readme.txt", []byte("hello")); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := disk.Get(ctx, "docs/readme.txt")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got) != "hello" {
			t.Fatalf("got %q", got)
		}
		exists, err := disk.Exists(ctx, "docs/readme.txt")
		if err != nil || !exists {
			t.Fatalf("exists=%v err=%v", exists, err)
		}
		if err := disk.Delete(ctx, "docs/readme.txt"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		missing, err := disk.Missing(ctx, "docs/readme.txt")
		if err != nil || !missing {
			t.Fatalf("missing=%v err=%v", missing, err)
		}
		if err := disk.Delete(ctx, "docs/readme.txt"); !errors.Is(err, filesystem.ErrNotFound) {
			t.Fatalf("delete missing error=%v", err)
		}
	})

	t.Run("stream copy move metadata", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		payload := bytes.Repeat([]byte("x"), 128*1024)
		if err := disk.Write(ctx, "large.bin", bytes.NewReader(payload)); err != nil {
			t.Fatalf("write: %v", err)
		}
		size, err := disk.Size(ctx, "large.bin")
		if err != nil || size != int64(len(payload)) {
			t.Fatalf("size=%d err=%v", size, err)
		}
		if _, err := disk.LastModified(ctx, "large.bin"); err != nil {
			t.Fatalf("last modified: %v", err)
		}
		if err := disk.Copy(ctx, "large.bin", "copy.bin"); err != nil {
			t.Fatalf("copy: %v", err)
		}
		if err := disk.Move(ctx, "copy.bin", "moved.bin"); err != nil {
			t.Fatalf("move: %v", err)
		}
		exists, _ := disk.Exists(ctx, "copy.bin")
		if exists {
			t.Fatalf("source still exists after move")
		}
		got, err := disk.Get(ctx, "moved.bin")
		if err != nil {
			t.Fatalf("get moved: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("moved content mismatch")
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		disk := newDisk(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := disk.Put(ctx, "x.txt", []byte("x"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	})
}

func RunDirectoryContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("make and delete directory", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		if err := disk.MakeDirectory(ctx, "photos/2026"); err != nil {
			t.Fatalf("make directory: %v", err)
		}
		info, err := disk.Stat(ctx, "photos/2026")
		if err != nil {
			t.Fatalf("stat directory: %v", err)
		}
		if !info.IsDir {
			t.Fatalf("expected directory")
		}
		if err := disk.Put(ctx, "photos/2026/a.jpg", []byte("jpg")); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := disk.DeleteDirectory(ctx, "photos"); err != nil {
			t.Fatalf("delete directory: %v", err)
		}
		exists, err := disk.Exists(ctx, "photos/2026/a.jpg")
		if err != nil || exists {
			t.Fatalf("exists=%v err=%v", exists, err)
		}
	})
}

func RunListContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("flat recursive and paged list", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		for _, path := range []string{"a.txt", "b.txt", "dir/c.txt", "dir/nested/d.txt"} {
			if err := disk.Put(ctx, path, []byte(path)); err != nil {
				t.Fatalf("put %s: %v", path, err)
			}
		}
		files, err := disk.Files(ctx, "")
		if err != nil {
			t.Fatalf("files: %v", err)
		}
		if strings.Join(files, ",") != "a.txt,b.txt" {
			t.Fatalf("files=%v", files)
		}
		all, err := disk.AllFiles(ctx, "")
		if err != nil {
			t.Fatalf("all files: %v", err)
		}
		if len(all) != 4 {
			t.Fatalf("all files=%v", all)
		}
		page, err := disk.ListPage(ctx, "", filesystem.WithPageSize(2))
		if err != nil {
			t.Fatalf("list page: %v", err)
		}
		if len(page.Items) != 2 || page.NextCursor == "" {
			t.Fatalf("page=%+v", page)
		}
		next, err := disk.ListPage(ctx, "", filesystem.WithPageSize(2), filesystem.WithCursor(page.NextCursor))
		if err != nil {
			t.Fatalf("next page: %v", err)
		}
		if len(next.Items) == 0 {
			t.Fatalf("expected next page")
		}
	})
}

func RunVisibilityContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("set and get visibility", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		if err := disk.Put(ctx, "public.txt", []byte("x"), filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
			t.Fatalf("put: %v", err)
		}
		visibility, err := disk.GetVisibility(ctx, "public.txt")
		if err != nil {
			t.Fatalf("get visibility: %v", err)
		}
		if visibility != filesystem.VisibilityPublic {
			t.Fatalf("visibility=%s", visibility)
		}
		if err := disk.SetVisibility(ctx, "public.txt", filesystem.VisibilityPrivate); err != nil {
			t.Fatalf("set visibility: %v", err)
		}
		visibility, err = disk.GetVisibility(ctx, "public.txt")
		if err != nil {
			t.Fatalf("get visibility private: %v", err)
		}
		if visibility != filesystem.VisibilityPrivate {
			t.Fatalf("visibility=%s", visibility)
		}
	})
}

func RunPathSafetyContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	badPaths := []string{
		"../x",
		"/x",
		"a/../../x",
		"a//b",
		"a/./b",
		"a/../b",
		"a\x00b",
		`a\b`,
		`C:\Windows`,
		"C:/Windows",
		`\\server\share`,
		"a:b",
		"CON",
		"NUL",
		"aux.txt",
	}
	for _, badPath := range badPaths {
		t.Run(badPath, func(t *testing.T) {
			disk := newDisk(t)
			err := disk.Put(context.Background(), badPath, []byte("x"))
			if !errors.Is(err, filesystem.ErrInvalidPath) {
				t.Fatalf("expected invalid path for %q, got %v", badPath, err)
			}
		})
	}
}

func RunURLContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("url escapes path segments", func(t *testing.T) {
		disk := newDisk(t)
		got, err := disk.URL(context.Background(), "dir/hello world#?.txt")
		if err != nil {
			t.Fatalf("url: %v", err)
		}
		if got != "/storage/dir/hello%20world%23%3F.txt" {
			t.Fatalf("url=%q", got)
		}
	})
}

func RunTemporaryURLContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("temporary url expiration", func(t *testing.T) {
		disk := newDisk(t)
		_, err := disk.TemporaryURL(context.Background(), "a.txt", time.Now().Add(-time.Minute))
		if !errors.Is(err, filesystem.ErrInvalidExpiration) {
			t.Fatalf("expected invalid expiration, got %v", err)
		}
	})
}

func RunReadOnlyContract(t *testing.T, newDisk func(t testing.TB) *filesystem.Disk) {
	t.Helper()
	t.Run("write operations return read only", func(t *testing.T) {
		disk := newDisk(t)
		ctx := context.Background()
		for name, fn := range map[string]func() error{
			"put":              func() error { return disk.Put(ctx, "a.txt", []byte("x")) },
			"delete":           func() error { return disk.Delete(ctx, "a.txt") },
			"copy":             func() error { return disk.Copy(ctx, "a.txt", "b.txt") },
			"move":             func() error { return disk.Move(ctx, "a.txt", "b.txt") },
			"make directory":   func() error { return disk.MakeDirectory(ctx, "dir") },
			"delete directory": func() error { return disk.DeleteDirectory(ctx, "dir") },
			"set visibility":   func() error { return disk.SetVisibility(ctx, "a.txt", filesystem.VisibilityPublic) },
		} {
			if err := fn(); !errors.Is(err, filesystem.ErrReadOnly) {
				t.Fatalf("%s: expected read only, got %v", name, err)
			}
		}
	})
}

func DrainIterator(t *testing.T, ctx context.Context, it filesystem.EntryIterator) []filesystem.Entry {
	t.Helper()
	defer it.Close()
	var entries []filesystem.Entry
	for {
		entry, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("iterator: %v", err)
		}
		entries = append(entries, entry)
	}
}
