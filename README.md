# go-filesystem

[![Go Reference](https://pkg.go.dev/badge/github.com/duolabmeng6/go-filesystem.svg)](https://pkg.go.dev/github.com/duolabmeng6/go-filesystem)

Go 文档地址：<https://pkg.go.dev/github.com/duolabmeng6/go-filesystem>

许可证：MIT

`go-filesystem` 最方便的地方是：业务代码只认一个 disk 别名，例如 `storage`；部署时通过配置把这个别名指向本地目录、S3/R2/B2/MinIO，或者阿里云 OSS。上传、下载、生成链接的业务代码不用跟着存储方式变化。

## 安装

```sh
go get github.com/duolabmeng6/go-filesystem
```

## 一个别名切换本地、S3、OSS

下面这个例子里，业务代码始终使用 `storage` 这个别名。你只需要改启动命令里的 `FILESYSTEM_DRIVER=local|s3|oss`，就能切换实际存储位置。

```sh
FILESYSTEM_DRIVER=local go run .
FILESYSTEM_DRIVER=s3 go run .
FILESYSTEM_DRIVER=oss go run .
```

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

	// storage 是业务别名；driver 决定 storage 最终指向本地、S3 还是 OSS。
	driver := os.Getenv("FILESYSTEM_DRIVER")
	if driver == "" {
		driver = "local"
	}

	storageConfig := filesystem.DiskConfig{Driver: "local", Root: "storage/app"}
	switch driver {
	case "s3":
		storageConfig = filesystem.DiskConfig{
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
		}
	case "oss":
		storageConfig = filesystem.DiskConfig{
			Driver:  "oss",
			BaseURL: os.Getenv("OSS_BASE_URL"),
			Options: map[string]any{
				"bucket":            os.Getenv("OSS_BUCKET"),
				"region":            os.Getenv("OSS_REGION"),
				"endpoint":          os.Getenv("OSS_ENDPOINT"),
				"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
				"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
			},
		}
	}

	manager, err := filesystem.NewFromConfig(
		ctx,
		filesystem.Config{
			Default: "storage",
			Disks: map[string]filesystem.DiskConfig{
				"storage": storageConfig,
			},
		},
		filesystem.WithDriver("local", local.NewFactory()),
		filesystem.WithDriver("s3", s3driver.NewFactory()),
		filesystem.WithDriver("oss", ossdriver.NewFactory()),
	)
	if err != nil {
		log.Fatal(err)
	}

	// 从这里开始就是稳定的业务代码：不关心文件最终写到本地、S3 还是 OSS。
	if err := manager.Put(ctx, "reports/hello.txt", []byte("hello")); err != nil {
		log.Fatal(err)
	}

	data, err := manager.Get(ctx, "reports/hello.txt")
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s", data)
}
```

## 常见配置

### 只用本地目录

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "storage",
	Disks: map[string]filesystem.DiskConfig{
		"storage": {
			Driver: "local",
			Root:   "storage/app",
		},
	},
}, filesystem.WithDriver("local", local.NewFactory()))
```

### 公开文件目录

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "storage",
	Disks: map[string]filesystem.DiskConfig{
		"storage": {
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

publicDisk, err := manager.Disk("public")
if err != nil {
	return err
}

if err := publicDisk.Put(ctx, "avatars/me.png", data, filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
	return err
}

url, err := publicDisk.URL(ctx, "avatars/me.png")
```

### 把 `storage` 别名切到 S3 兼容存储

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "storage",
	Disks: map[string]filesystem.DiskConfig{
		"storage": {
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
if err != nil {
	return err
}

err = manager.Put(ctx, "uploads/a.txt", []byte("hello"))
```

### 把 `storage` 别名切到阿里云 OSS

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "storage",
	Disks: map[string]filesystem.DiskConfig{
		"storage": {
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
if err != nil {
	return err
}

err = manager.Put(ctx, "uploads/a.txt", []byte("hello"))
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
