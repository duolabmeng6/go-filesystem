# go-filesystem

[![Go Reference](https://pkg.go.dev/badge/github.com/duolabmeng6/go-filesystem.svg)](https://pkg.go.dev/github.com/duolabmeng6/go-filesystem)

Go 文档地址：<https://pkg.go.dev/github.com/duolabmeng6/go-filesystem>

许可证：MIT

`go-filesystem` 最方便的地方是：你可以同时配置本地目录、S3/R2/B2/MinIO、阿里云 OSS，然后明确指定这次文件要写到哪个 disk。

```go
if err := manager.MustDisk("local").Put(ctx, "reports/a.txt", data); err != nil {
	return err
}

if err := manager.MustDisk("s3").Put(ctx, "reports/a.txt", data); err != nil {
	return err
}

if err := manager.MustDisk("oss").Put(ctx, "reports/a.txt", data); err != nil {
	return err
}
```

## 安装

```sh
go get github.com/duolabmeng6/go-filesystem
```

## 同时配置 local、S3、OSS

先配置三个 disk：

```go
manager, err := filesystem.NewFromConfig(
	ctx,
	filesystem.Config{
		Default: "local",
		Disks: map[string]filesystem.DiskConfig{
			"local": {
				Driver: "local",
				Root:   "storage/app",
			},
			"s3": {
				Driver:  "s3",
				BaseURL: os.Getenv("S3_BASE_URL"),
				Options: map[string]any{
					"bucket":            os.Getenv("S3_BUCKET"),
					"region":            os.Getenv("S3_REGION"),
					"endpoint":          os.Getenv("S3_ENDPOINT"),
					"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
					"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
					"use_path_style":    true,
					"disable_acl":       true,
				},
			},
			"oss": {
				Driver:  "oss",
				BaseURL: os.Getenv("OSS_BASE_URL"),
				Options: map[string]any{
					"bucket":            os.Getenv("OSS_BUCKET"),
					"region":            os.Getenv("OSS_REGION"),
					"endpoint":          os.Getenv("OSS_ENDPOINT"),
					"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
					"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
				},
			},
		},
	},
	filesystem.WithDriver("local", local.NewFactory()),
	filesystem.WithDriver("s3", s3driver.NewFactory()),
	filesystem.WithDriver("oss", ossdriver.NewFactory()),
)
```

再指定写到哪里：

```go
if err := manager.MustDisk("local").Put(ctx, "reports/local.txt", []byte("local")); err != nil {
	return err
}

if err := manager.MustDisk("s3").Put(ctx, "reports/s3.txt", []byte("s3")); err != nil {
	return err
}

if err := manager.MustDisk("oss").Put(ctx, "reports/oss.txt", []byte("oss")); err != nil {
	return err
}
```

如果你想通过命令切换，也只是把 disk 名称换成参数。disk 名称来自外部输入时，用 `Disk` 接住错误：

```go
diskName := os.Getenv("FILESYSTEM_DISK")
if diskName == "" {
	diskName = "local"
}

disk, err := manager.Disk(diskName)
if err != nil {
	return err
}

err = disk.Put(ctx, "reports/a.txt", data)
```

## 完整示例

```go
package main

import (
	"context"
	"log"
	"os"

	"github.com/duolabmeng6/go-filesystem"
	ossdriver "github.com/duolabmeng6/go-filesystem/drivers/oss"
	s3driver "github.com/duolabmeng6/go-filesystem/drivers/s3"
	"github.com/duolabmeng6/go-filesystem/local"
)

func main() {
	ctx := context.Background()

	manager, err := filesystem.NewFromConfig(
		ctx,
		filesystem.Config{
			Default: "local",
			Disks: map[string]filesystem.DiskConfig{
				"local": {
					Driver: "local",
					Root:   "storage/app",
				},
				"s3": {
					Driver:  "s3",
					BaseURL: os.Getenv("S3_BASE_URL"),
					Options: map[string]any{
						"bucket":            os.Getenv("S3_BUCKET"),
						"region":            os.Getenv("S3_REGION"),
						"endpoint":          os.Getenv("S3_ENDPOINT"),
						"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
						"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
						"use_path_style":    true,
						"disable_acl":       true,
					},
				},
				"oss": {
					Driver:  "oss",
					BaseURL: os.Getenv("OSS_BASE_URL"),
					Options: map[string]any{
						"bucket":            os.Getenv("OSS_BUCKET"),
						"region":            os.Getenv("OSS_REGION"),
						"endpoint":          os.Getenv("OSS_ENDPOINT"),
						"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
						"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
					},
				},
			},
		},
		filesystem.WithDriver("local", local.NewFactory()),
		filesystem.WithDriver("s3", s3driver.NewFactory()),
		filesystem.WithDriver("oss", ossdriver.NewFactory()),
	)
	if err != nil {
		log.Fatal(err)
	}

	if err := manager.MustDisk("local").Put(ctx, "reports/local.txt", []byte("local")); err != nil {
		log.Fatal(err)
	}

	if err := manager.MustDisk("s3").Put(ctx, "reports/s3.txt", []byte("s3")); err != nil {
		log.Fatal(err)
	}

	if err := manager.MustDisk("oss").Put(ctx, "reports/oss.txt", []byte("oss")); err != nil {
		log.Fatal(err)
	}
}
```

如果你的业务只需要一个默认存储，也可以直接用 `manager.Put` / `manager.Get`。

## 常见配置

### 只用本地目录

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "local",
	Disks: map[string]filesystem.DiskConfig{
		"local": {
			Driver: "local",
			Root:   "storage/app",
		},
	},
}, filesystem.WithDriver("local", local.NewFactory()))
```

### 本地公开目录

```go
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
	return err
}

if err := manager.MustDisk("public").Put(ctx, "avatars/me.png", data, filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
	return err
}

url, err := manager.MustDisk("public").URL(ctx, "avatars/me.png")
```

### 配置 S3 兼容存储

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "s3",
	Disks: map[string]filesystem.DiskConfig{
		"s3": {
			Driver:  "s3",
			BaseURL: os.Getenv("S3_BASE_URL"),
			Options: map[string]any{
				"bucket":            os.Getenv("S3_BUCKET"),
				"region":            os.Getenv("S3_REGION"),
				"endpoint":          os.Getenv("S3_ENDPOINT"),
				"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
				"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
				"use_path_style":    true,
				"disable_acl":       true,
			},
		},
	},
}, filesystem.WithDriver("s3", s3driver.NewFactory()))
```

### 配置阿里云 OSS

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "oss",
	Disks: map[string]filesystem.DiskConfig{
		"oss": {
			Driver:  "oss",
			BaseURL: os.Getenv("OSS_BASE_URL"),
			Options: map[string]any{
				"bucket":            os.Getenv("OSS_BUCKET"),
				"region":            os.Getenv("OSS_REGION"),
				"endpoint":          os.Getenv("OSS_ENDPOINT"),
				"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
				"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
			},
		},
	},
}, filesystem.WithDriver("oss", ossdriver.NewFactory()))
```

## 中文教程

- [文档目录](docs/目录.md)
- [快速入门](docs/快速入门.md)
- [配置本地磁盘](docs/配置本地磁盘.md)
- [配置对象存储](docs/配置对象存储.md)
- [上传下载与文件管理](docs/上传下载与文件管理.md)
- [生成公开链接和临时链接](docs/生成链接.md)
- [在测试中使用 Fake](docs/测试.md)
- [部署到生产环境](docs/部署.md)
- [常见问题](docs/常见问题.md)

## 运行测试

```sh
go test ./...
```
