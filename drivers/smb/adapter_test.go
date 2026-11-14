package smb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

func TestPathsRejectTraversal(t *testing.T) {
	a := &Adapter{root: "projects"}
	for _, p := range []string{"../outside", "/absolute", "a/../b", `a\b`, "a:stream"} {
		if _, err := a.target(p); !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Errorf("target(%q): expected invalid path, got %v", p, err)
		}
	}
	if got, err := a.target("docs/file.md"); err != nil || got != "projects/docs/file.md" {
		t.Fatalf("unexpected target %q: %v", got, err)
	}
}

func TestSMBIntegration(t *testing.T) {
	host := os.Getenv("AFILE_SMB_TEST_HOST")
	if host == "" {
		t.Skip("SMB test server not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	port := 445
	if value := os.Getenv("AFILE_SMB_TEST_PORT"); value != "" {
		var err error
		port, err = strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := New(ctx, Config{Host: host, Port: port, Share: os.Getenv("AFILE_SMB_TEST_SHARE"), Username: os.Getenv("AFILE_SMB_TEST_USER"), Password: os.Getenv("AFILE_SMB_TEST_PASSWORD"), Domain: os.Getenv("AFILE_SMB_TEST_DOMAIN")})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	dir := "test-" + time.Now().Format("150405.000000000")
	if err = a.MakeDirectory(ctx, dir, filesystem.DirectoryOptions{}); err != nil {
		t.Fatal(err)
	}
	defer a.DeleteDirectory(context.Background(), dir)
	filename := dir + "/file.txt"
	if err = a.Write(ctx, filename, strings.NewReader("first"), filesystem.DefaultWriteOptions()); err != nil {
		t.Fatal(err)
	}
	noOverwrite := filesystem.DefaultWriteOptions()
	noOverwrite.Overwrite = false
	if err = a.Write(ctx, filename, strings.NewReader("should not replace"), noOverwrite); !errors.Is(err, filesystem.ErrAlreadyExists) {
		t.Fatalf("expected existing file error: %v", err)
	}
	if err = a.Write(ctx, dir+"/new.txt", strings.NewReader("new"), noOverwrite); err != nil {
		t.Fatal(err)
	}
	if err = a.Delete(ctx, dir+"/new.txt"); err != nil {
		t.Fatal(err)
	}
	if err = a.Write(ctx, filename, strings.NewReader("second"), filesystem.DefaultWriteOptions()); err != nil {
		t.Fatal(err)
	}
	f, err := a.Open(ctx, filename)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || !bytes.Equal(data, []byte("second")) {
		t.Fatalf("read: %q %v", data, err)
	}
	page, err := a.ListPage(ctx, dir, filesystem.ListOptions{})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list: %#v %v", page, err)
	}
	if err = a.Copy(ctx, filename, dir+"/copy.txt"); err != nil {
		t.Fatal(err)
	}
	if err = a.Move(ctx, dir+"/copy.txt", dir+"/moved.txt"); err != nil {
		t.Fatal(err)
	}
	if err = a.Delete(ctx, dir+"/moved.txt"); err != nil {
		t.Fatal(err)
	}
}

func TestPortOption(t *testing.T) {
	for _, value := range []any{445, int64(445), float64(445), json.Number("445"), "445"} {
		got, err := portOption(value)
		if err != nil || got != 445 {
			t.Errorf("port %v: %d %v", value, got, err)
		}
	}
	for _, value := range []any{"invalid", -1, 65536, 4.5, true} {
		if _, err := portOption(value); !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Errorf("port %v should be rejected: %v", value, err)
		}
	}
}
