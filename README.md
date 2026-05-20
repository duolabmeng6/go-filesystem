# go-filesystem

`go-filesystem` is a Go file storage abstraction inspired by Laravel Filesystem / Storage concepts: disks, drivers, default disks, public URLs, visibility, temporary URLs, scoped disks, read-only disks, and fakes for tests.

The API is Go-first: explicit managers and disks, `context.Context`, streaming I/O, normal `error` returns, and no cloud SDK dependencies in the core package.

## Install

```sh
go get github.com/duolabmeng6/go-filesystem
```

## Configure a Manager

```go
package main

import (
	"context"
	"log"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/local"
)

func main() {
	ctx := context.Background()

	manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
		Default: "local",
		Disks: map[string]filesystem.DiskConfig{
			"local": {
				Driver: "local",
				Root:   "storage/app/private",
			},
			"public": {
				Driver:     "local",
				Root:       "storage/app/public",
				BaseURL:    "/storage",
				Visibility: filesystem.VisibilityPublic,
			},
		},
	}, filesystem.WithDriver("local", local.NewFactory()))
	if err != nil {
		log.Fatal(err)
	}

	if err := manager.Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
		log.Fatal(err)
	}
}
```

`BaseURL` only generates URL strings. Your application still needs an HTTP server, reverse proxy, framework route, or CDN that serves the configured root directory.

## Named Disks

```go
publicDisk, err := manager.Disk("public")
if err != nil {
	return err
}

if err := publicDisk.Put(ctx, "avatars/me.png", data, filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
	return err
}

url, err := publicDisk.URL(ctx, "avatars/me.png")
```

URL paths are escaped per segment, so `docs/hello world#1.txt` becomes `/storage/docs/hello%20world%231.txt`.

## Streaming I/O

```go
file, err := os.Open("large-video.mp4")
if err != nil {
	return err
}
defer file.Close()

err = manager.Write(ctx, "videos/large-video.mp4", file)
```

## Temporary URLs

The local driver supports temporary URLs only when a builder is configured.

```go
disk, err := local.NewDisk(local.Config{
	Root: "/srv/private",
	TemporaryURLBuilder: func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
		return signDownloadURL(path, expiresAt), nil
	},
})
if err != nil {
	return err
}

url, err := disk.TemporaryURL(ctx, "invoices/a.pdf", time.Now().Add(15*time.Minute))
```

## Scoped and Read-Only Disks

```go
base, err := local.New(local.Config{Root: "storage/app"})
if err != nil {
	return err
}

scopedAdapter, err := filesystem.Scoped(base, "tenants/acme")
if err != nil {
	return err
}

tenantDisk := filesystem.NewDisk(scopedAdapter)
readOnlyDisk := filesystem.NewDisk(filesystem.ReadOnly(scopedAdapter))
```

`Scoped` applies a prefix before calling the underlying adapter and strips the prefix from list results. `ReadOnly` preserves read capabilities and returns `ErrReadOnly` for writes, deletes, moves, directory changes, and visibility changes.

## Fakes for Tests

```go
func TestUpload(t *testing.T) {
	manager := filesystem.New(filesystem.WithDefaultDisk("local"))
	manager.MustExtend("local", local.NewFactory())
	_ = manager.ConfigureDisk("local", filesystem.DiskConfig{Driver: "local", Root: t.TempDir()})

	fake := filesystem.Fake(t, manager, "local")

	err := manager.Put(context.Background(), "avatars/me.png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}

	fake.AssertExists("avatars/me.png")
	fake.AssertContent("avatars/me.png", []byte("png"))
}
```

`Fake` restores the previous disk during `t.Cleanup`. `PersistentFake` uses a root you provide and keeps files on disk for debugging.

## Local Driver Notes

- Paths are object-storage style slash paths, not OS absolute paths.
- Empty paths are accepted only for listing the root.
- `..`, `.`, repeated slashes, backslashes, NUL/control characters, Windows drive/UNC paths, colon segments, and Windows reserved names are rejected.
- Local roots are resolved at initialization; existing symlink segments under the root are rejected for reads, writes, deletes, and listing.
- Symlink protection is a best-effort application-level guard, not a strong sandbox.
- Writes are atomic by default: data is written to a temporary file in the destination directory, chmod is applied, then the file is renamed into place.
- `WithOverwrite(false)` makes writes fail with `ErrAlreadyExists` when the target file already exists; it is not a cross-process race-free lock.
- Local filesystem calls are best-effort with context cancellation; long streaming copies check context between read/write chunks.

## Driver Contracts

The `filesystemtest` package contains reusable contract tests for drivers:

```go
func TestDriverContracts(t *testing.T) {
	filesystemtest.RunObjectContract(t, newDisk)
	filesystemtest.RunDirectoryContract(t, newDisk)
	filesystemtest.RunListContract(t, newDisk)
	filesystemtest.RunVisibilityContract(t, newDisk)
	filesystemtest.RunPathSafetyContract(t, newDisk)
}
```

Future S3, SFTP, OSS, COS, Qiniu, and other drivers can live in separate packages or modules and register with `Manager.Extend`.

## AWS S3

S3 support lives outside the core package in `drivers/s3` and uses AWS SDK for Go v2.

```go
import s3driver "github.com/duolabmeng6/go-filesystem/drivers/s3"

manager := filesystem.New(filesystem.WithDefaultDisk("s3"))
manager.MustExtend("s3", s3driver.NewFactory())

_ = manager.ConfigureDisk("s3", filesystem.DiskConfig{
	Driver:  "s3",
	BaseURL: "https://cdn.example.com",
	Options: map[string]any{
		"bucket":            "my-bucket",
		"region":            "us-east-1",
		"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
		"use_path_style":    false,
	},
})
```

Typed config is preferred in Go code:

```go
disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          "my-bucket",
	Region:          "us-east-1",
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	BaseURL:         "https://cdn.example.com",
	Visibility:      filesystem.VisibilityPrivate,
})
```

For S3-compatible services, set `Endpoint` and usually `UsePathStyle`.

## Aliyun OSS

OSS support lives outside the core package in `drivers/oss`.

```go
import ossdriver "github.com/duolabmeng6/go-filesystem/drivers/oss"

manager := filesystem.New(filesystem.WithDefaultDisk("oss"))
manager.MustExtend("oss", ossdriver.NewFactory())

_ = manager.ConfigureDisk("oss", filesystem.DiskConfig{
	Driver:  "oss",
	BaseURL: "https://cdn.example.com",
	Options: map[string]any{
		"bucket":            "my-bucket",
		"region":            "cn-hangzhou",
		"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
	},
})
```

For Go code, prefer the typed config:

```go
disk, err := ossdriver.NewDisk(ossdriver.Config{
	Bucket:          "my-bucket",
	Region:          "cn-hangzhou",
	AccessKeyID:     os.Getenv("OSS_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("OSS_ACCESS_KEY_SECRET"),
	BaseURL:         "https://cdn.example.com",
	Visibility:      filesystem.VisibilityPrivate,
})
```
