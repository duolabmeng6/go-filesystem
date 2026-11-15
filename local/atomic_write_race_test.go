package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

// Both writers reach Read only after the initial existence check has passed.
type gatedReader struct {
	once    sync.Once
	ready   chan<- struct{}
	release <-chan struct{}
	reader  io.Reader
}

func (r *gatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		r.ready <- struct{}{}
		<-r.release
	})
	return r.reader.Read(p)
}

func TestAtomicWriteWithoutOverwriteCommitsOnlyOneConcurrentWriter(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	type result struct {
		content string
		err     error
	}
	results := make(chan result, 2)
	for _, content := range []string{"first content", "second content"} {
		go func(content string) {
			r := &gatedReader{ready: ready, release: release, reader: strings.NewReader(content)}
			err := disk.Write(context.Background(), "same.txt", r, filesystem.WithAtomicWrite(true), filesystem.WithOverwrite(false))
			results <- result{content: content, err: err}
		}(content)
	}
	for range 2 {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			t.Fatal("writers did not both reach the write barrier")
		}
	}
	close(release)
	var winner string
	successes, conflicts := 0, 0
	for range 2 {
		select {
		case got := <-results:
			switch {
			case got.err == nil:
				successes++
				winner = got.content
			case errors.Is(got.err, filesystem.ErrAlreadyExists):
				conflicts++
			default:
				t.Fatalf("unexpected write error: %v", got.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent write did not finish")
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	content, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil || string(content) != winner {
		t.Fatalf("content=%q winner=%q error=%v", content, winner, err)
	}
	assertNoAtomicTemps(t, root)
}

func TestAtomicWriteWithoutOverwritePreservesFileCreatedDuringWrite(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	result := make(chan error, 1)
	go func() {
		r := &gatedReader{ready: ready, release: release, reader: strings.NewReader("replacement")}
		result <- disk.Write(context.Background(), "same.txt", r, filesystem.WithOverwrite(false))
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not reach the write barrier")
	}
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, filesystem.ErrAlreadyExists) {
			t.Fatalf("expected already exists, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("write did not finish")
	}
	content, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil || string(content) != "original" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	assertNoAtomicTemps(t, root)
}

func TestAtomicWriteWithOverwriteReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "same.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := disk.Write(context.Background(), "same.txt", strings.NewReader("replacement"), filesystem.WithAtomicWrite(true), filesystem.WithOverwrite(true)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil || string(content) != "replacement" {
		t.Fatalf("content=%q error=%v", content, err)
	}
	assertNoAtomicTemps(t, root)
}

func TestAtomicWriteCancellationDoesNotPublishOrLeaveTemporaryFile(t *testing.T) {
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	result := make(chan error, 1)
	go func() {
		r := &gatedReader{ready: ready, release: release, reader: strings.NewReader("content")}
		result <- disk.Write(ctx, "cancelled.txt", r, filesystem.WithOverwrite(false))
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not reach the write barrier")
	}
	cancel()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled write did not finish")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled file unexpectedly exists: %v", err)
	}
	assertNoAtomicTemps(t, root)
}

func assertNoAtomicTemps(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file was not removed: %s", entry.Name())
		}
	}
}
