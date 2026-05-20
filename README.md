# go-filesystem

[![Go Reference](https://pkg.go.dev/badge/github.com/duolabmeng6/go-filesystem.svg)](https://pkg.go.dev/github.com/duolabmeng6/go-filesystem)

Go 文档地址：<https://pkg.go.dev/github.com/duolabmeng6/go-filesystem>

许可证：MIT

## 安装

```sh
go get github.com/duolabmeng6/go-filesystem
```

## 30 秒写入一个文件

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
				Root:   "storage/app",
			},
		},
	}, filesystem.WithDriver("local", local.NewFactory()))
	if err != nil {
		log.Fatal(err)
	}

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

## 常见用法

### 使用公开文件目录

```go
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
	Default: "private",
	Disks: map[string]filesystem.DiskConfig{
		"private": {
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

### 使用 S3 兼容存储

```go
import s3driver "github.com/duolabmeng6/go-filesystem/drivers/s3"

disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          os.Getenv("S3_BUCKET"),
	Region:          os.Getenv("S3_REGION"),
	Endpoint:        os.Getenv("S3_ENDPOINT"),
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	BaseURL:         os.Getenv("S3_BASE_URL"),
	UsePathStyle:    true,
	DisableACL:      true,
})
if err != nil {
	return err
}

if err := disk.Put(ctx, "uploads/a.txt", []byte("hello")); err != nil {
	return err
}
```

### 使用阿里云 OSS

```go
import ossdriver "github.com/duolabmeng6/go-filesystem/drivers/oss"

disk, err := ossdriver.NewDisk(ossdriver.Config{
	Bucket:          os.Getenv("OSS_BUCKET"),
	Region:          os.Getenv("OSS_REGION"),
	Endpoint:        os.Getenv("OSS_ENDPOINT"),
	AccessKeyID:     os.Getenv("OSS_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("OSS_ACCESS_KEY_SECRET"),
	BaseURL:         os.Getenv("OSS_BASE_URL"),
})
if err != nil {
	return err
}

url, err := disk.TemporaryURL(ctx, "private/invoice.pdf", time.Now().Add(15*time.Minute))
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
