package local

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
)

func TestMultipartConcurrentReplacementPublishesMatchingData(t *testing.T) {
	ctx := context.Background()
	disk := newTestDisk(t)
	a := disk.Adapter().(*Adapter)
	upload, err := disk.CreateMultipartUpload(ctx, "result")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
	const writers, iterations = 4, 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				content := fmt.Sprintf("writer %d iteration %d", writer, j)
				if _, err := disk.UploadPart(ctx, upload.Path, upload.UploadID, 1, strings.NewReader(content), int64(len(content))); err != nil {
					errs <- err
					return
				}
				lock, err := a.lockMultipartPart(ctx, upload.UploadID, 1)
				if err != nil {
					errs <- err
					return
				}
				var meta multipartPartMetadata
				if err := readJSON(a.multipartPartMetaPath(upload.UploadID, 1), &meta); err != nil {
					_ = lock.Close()
					errs <- err
					return
				}
				data, err := os.ReadFile(filepath.Join(a.multipartDir(upload.UploadID), meta.DataFile))
				_ = lock.Close()
				if err != nil {
					errs <- err
					return
				}
				sum := md5.Sum(data)
				if int64(len(data)) != meta.Size || hex.EncodeToString(sum[:]) != meta.ETag {
					errs <- fmt.Errorf("manifest does not describe data")
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	entries, err := os.ReadDir(a.multipartDir(upload.UploadID))
	if err != nil {
		t.Fatal(err)
	}
	dataFiles := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "part-000001-") {
			dataFiles++
		}
	}
	if dataFiles != 1 {
		t.Fatalf("replacement left %d part versions", dataFiles)
	}
	listed, err := disk.ListMultipartUploadParts(ctx, upload.Path, upload.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, listed); err != nil {
		t.Fatal(err)
	}
	data, err := disk.Get(ctx, upload.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(data)
	if len(listed) != 1 || listed[0].ETag != hex.EncodeToString(sum[:]) {
		t.Fatal("completed content differs from listed part")
	}
}

func TestMultipartRejectsStaleOrCorruptedParts(t *testing.T) {
	ctx := context.Background()
	disk := newTestDisk(t)
	a := disk.Adapter().(*Adapter)
	if err := disk.Put(ctx, "result", []byte("original")); err != nil {
		t.Fatal(err)
	}
	upload, err := disk.CreateMultipartUpload(ctx, "result")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
	old, err := disk.UploadPart(ctx, upload.Path, upload.UploadID, 1, strings.NewReader("old"), 3)
	if err != nil {
		t.Fatal(err)
	}
	current, err := disk.UploadPart(ctx, upload.Path, upload.UploadID, 1, strings.NewReader("new"), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, []filesystem.MultipartUploadPart{old}); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("stale ETag: %v", err)
	}
	var meta multipartPartMetadata
	if err := readJSON(a.multipartPartMetaPath(upload.UploadID, 1), &meta); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.multipartDir(upload.UploadID), meta.DataFile), []byte("BAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, []filesystem.MultipartUploadPart{current}); !errors.Is(err, filesystem.ErrInvalidPath) {
		t.Fatalf("corrupted part: %v", err)
	}
	data, err := disk.Get(ctx, upload.Path)
	if err != nil || string(data) != "original" {
		t.Fatalf("target=%q err=%v", data, err)
	}
}

func TestMultipartLegacyPartStillCompletes(t *testing.T) {
	ctx := context.Background()
	disk := newTestDisk(t)
	a := disk.Adapter().(*Adapter)
	upload, err := disk.CreateMultipartUpload(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
	data := []byte("legacy data")
	sum := md5.Sum(data)
	meta := multipartPartMetadata{PartNumber: 1, Size: int64(len(data)), ETag: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(a.multipartPartPath(upload.UploadID, 1), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(a.multipartPartMetaPath(upload.UploadID, 1), meta); err != nil {
		t.Fatal(err)
	}
	if err := disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, nil); err != nil {
		t.Fatal(err)
	}
	got, err := disk.Get(ctx, upload.Path)
	if err != nil || string(got) != string(data) {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestMultipartCrossProcessReplacement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := disk.CreateMultipartUpload(ctx, "result")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 4)
	for i := range commands {
		cmd := exec.Command(executable, "-test.run=^TestMultipartProcessHelper$")
		cmd.Env = append(os.Environ(), "AFILE_MULTIPART_TEST_ROOT="+root, "AFILE_MULTIPART_TEST_UPLOAD="+upload.UploadID, fmt.Sprintf("AFILE_MULTIPART_TEST_WRITER=%d", i))
		commands[i] = cmd
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	parts, err := disk.ListMultipartUploadParts(ctx, upload.Path, upload.UploadID)
	if err != nil || len(parts) != 1 {
		t.Fatalf("parts=%v err=%v", parts, err)
	}
	if err := disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, parts); err != nil {
		t.Fatal(err)
	}
	data, err := disk.Get(ctx, upload.Path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(data)
	if parts[0].ETag != hex.EncodeToString(sum[:]) {
		t.Fatal("completed content differs from cross-process listed part")
	}
}

func TestMultipartProcessHelper(t *testing.T) {
	root := os.Getenv("AFILE_MULTIPART_TEST_ROOT")
	if root == "" {
		return
	}
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		data := fmt.Sprintf("process %s part %d", os.Getenv("AFILE_MULTIPART_TEST_WRITER"), i)
		_, err := disk.UploadPart(context.Background(), "result", os.Getenv("AFILE_MULTIPART_TEST_UPLOAD"), 1, strings.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestMultipartTerminationWaitsForActiveUpload(t *testing.T) {
	for _, operation := range []string{"complete", "abort"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			disk := newTestDisk(t)
			upload, err := disk.CreateMultipartUpload(ctx, "result")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
			ready := make(chan struct{}, 1)
			release := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				_, err := disk.UploadPart(ctx, upload.Path, upload.UploadID, 1, &gatedReader{ready: ready, release: release, reader: strings.NewReader("part")}, 4)
				result <- err
			}()
			<-ready
			deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			defer cancel()
			if operation == "abort" {
				err = disk.AbortMultipartUpload(deadline, upload.Path, upload.UploadID)
			} else {
				err = disk.CompleteMultipartUpload(deadline, upload.Path, upload.UploadID, nil)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				close(release)
				<-result
				t.Fatalf("operation should wait: %v", err)
			}
			close(release)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			parts, err := disk.ListMultipartUploadParts(ctx, upload.Path, upload.UploadID)
			if err != nil || len(parts) != 1 {
				t.Fatalf("parts=%v err=%v", parts, err)
			}
			if operation == "abort" {
				err = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID)
			} else {
				err = disk.CompleteMultipartUpload(ctx, upload.Path, upload.UploadID, parts)
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMultipartLockAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	upload, err := disk.CreateMultipartUpload(ctx, "result")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID) })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestMultipartLockProcessHelper$")
	cmd.Env = append(os.Environ(), "AFILE_MULTIPART_LOCK_ROOT="+root, "AFILE_MULTIPART_LOCK_UPLOAD="+upload.UploadID)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper did not acquire lock: %q %v", line, err)
	}
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	err = disk.AbortMultipartUpload(deadline, upload.Path, upload.UploadID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cross-process lock did not block: %v", err)
	}
	if _, err := stdin.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := disk.AbortMultipartUpload(ctx, upload.Path, upload.UploadID); err != nil {
		t.Fatal(err)
	}
}

func TestMultipartLockProcessHelper(t *testing.T) {
	root := os.Getenv("AFILE_MULTIPART_LOCK_ROOT")
	if root == "" {
		return
	}
	disk, err := NewDisk(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := disk.Adapter().(*Adapter).lockMultipartSession(context.Background(), os.Getenv("AFILE_MULTIPART_LOCK_UPLOAD"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	_, _ = fmt.Fprintln(os.Stdout, "ready")
	_, _ = io.CopyN(io.Discard, os.Stdin, 1)
}
