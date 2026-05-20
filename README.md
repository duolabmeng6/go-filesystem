# go-filesystem

`go-filesystem` 是一个 Go-first 的文件存储抽象库，用一套稳定 API 连接本地磁盘、S3 兼容对象存储、阿里云 OSS、只读磁盘、租户前缀、公开 URL、临时下载 URL 和测试 Fake。

它借鉴了 Laravel Filesystem / Storage 的概念，但 API 按 Go 的习惯设计：显式 `context.Context`、流式 `io.Reader` / `io.ReadCloser`、普通 `error` 返回、小接口、无全局状态依赖，以及可独立扩展的驱动包。

## 安装

```sh
go get github.com/duolabmeng6/go-filesystem
```

Go 文档地址：<https://pkg.go.dev/github.com/duolabmeng6/go-filesystem>

常用包：

```go
import (
	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/local"
	s3driver "github.com/duolabmeng6/go-filesystem/drivers/s3"
	ossdriver "github.com/duolabmeng6/go-filesystem/drivers/oss"
)
```

核心包的公开 API 不绑定云厂商 SDK；云存储能力放在 `drivers/s3` 和 `drivers/oss`。

## 快速开始

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

	if err := manager.Put(ctx, "reports/hello.txt", []byte("hello")); err != nil {
		log.Fatal(err)
	}

	publicDisk, err := manager.Disk("public")
	if err != nil {
		log.Fatal(err)
	}
	if err := publicDisk.Put(ctx, "avatars/me.png", []byte("png"), filesystem.WithVisibility(filesystem.VisibilityPublic)); err != nil {
		log.Fatal(err)
	}

	url, err := publicDisk.URL(ctx, "avatars/me.png")
	if err != nil {
		log.Fatal(err)
	}
	log.Println(url) // /storage/avatars/me.png
}
```

## 核心能力

- `Manager`：管理默认 disk、命名 disk、驱动注册、懒加载构建和测试替换。
- `Disk`：业务代码使用的读写、列表、复制、移动、元数据、visibility、URL 和临时 URL API。
- `Adapter`：驱动接口，本地磁盘、S3、OSS 或自定义存储都实现它。
- `Scoped`：给底层 adapter 自动加路径前缀，例如 `tenants/acme`。
- `ReadOnly`：保留读取和 URL 能力，写入、删除、移动等变更操作返回 `ErrReadOnly`。
- `Fake`：测试中替换 manager 的某个 disk，并在 `t.Cleanup` 自动恢复。
- `filesystemtest`：给自定义驱动复用的合约测试。

## 已支持驱动

| 驱动 | 包 | 说明 |
| --- | --- | --- |
| Local | `local` | 真实目录、默认原子写入、基于权限的 visibility、可选临时 URL builder。 |
| AWS S3 / S3-compatible | `drivers/s3` | AWS SDK for Go v2、自定义 endpoint、path-style、临时 URL、可禁用 ACL。 |
| Aliyun OSS | `drivers/oss` | OSS SDK v2、对象 ACL visibility、公开 URL、临时下载 URL。 |

Cloudflare R2、Backblaze B2 等 S3-compatible 服务通常应设置 `DisableACL: true`，公开/私有访问交给 bucket policy、CDN 或应用鉴权处理。

## 常用操作

```go
ctx := context.Background()

err := disk.Put(ctx, "docs/readme.txt", []byte("hello"))
data, err := disk.Get(ctx, "docs/readme.txt")

file, err := os.Open("large-video.mp4")
if err == nil {
	defer file.Close()
	err = disk.Write(ctx, "videos/large-video.mp4", file)
}

exists, err := disk.Exists(ctx, "docs/readme.txt")
info, err := disk.Stat(ctx, "docs/readme.txt")
mime, err := disk.MimeType(ctx, "docs/readme.txt")

err = disk.Copy(ctx, "docs/readme.txt", "docs/readme-copy.txt")
err = disk.Move(ctx, "docs/readme-copy.txt", "archive/readme.txt")
err = disk.DeleteIfExists(ctx, "archive/readme.txt")

files, err := disk.AllFiles(ctx, "docs")
url, err := disk.URL(ctx, "docs/readme.txt")
temporaryURL, err := disk.TemporaryURL(ctx, "docs/readme.txt", time.Now().Add(15*time.Minute))
```

路径是对象存储风格的相对路径，不是 OS 绝对路径。推荐使用 `avatars/user-1.png` 这种 slash path；库会拒绝绝对路径、`..`、`.`、重复斜杠、反斜杠、控制字符、Windows drive/UNC 路径、冒号段和 Windows 保留名。

## 配置示例

```go
manager := filesystem.New(filesystem.WithDefaultDisk("s3"))
manager.MustExtend("local", local.NewFactory())
manager.MustExtend("s3", s3driver.NewFactory())

err := manager.ConfigureDisk("s3", filesystem.DiskConfig{
	Driver:  "s3",
	BaseURL: "https://cdn.example.com",
	Options: map[string]any{
		"bucket":            "my-bucket",
		"region":            "us-east-1",
		"endpoint":          "https://s3.example.com",
		"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
		"use_path_style":    true,
		"disable_acl":       true,
	},
})
```

在纯 Go 代码里，也可以优先使用驱动的强类型配置：

```go
disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          "my-bucket",
	Region:          "us-east-1",
	Endpoint:        "https://s3.example.com",
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	BaseURL:         "https://cdn.example.com",
	UsePathStyle:    true,
	DisableACL:      true,
})
```

`BaseURL` 只负责生成 URL 字符串。真正能否访问，取决于应用路由、反向代理、对象存储公开策略或 CDN 配置。

## 测试

```sh
go test ./...
```

业务测试可以用 Fake 替换配置好的 disk：

```go
func TestUpload(t *testing.T) {
	manager := filesystem.New(filesystem.WithDefaultDisk("local"))
	manager.MustExtend("local", local.NewFactory())
	_ = manager.ConfigureDisk("local", filesystem.DiskConfig{Driver: "local", Root: t.TempDir()})

	fake := filesystem.Fake(t, manager, "local")

	if err := manager.Put(context.Background(), "avatars/me.png", []byte("png")); err != nil {
		t.Fatal(err)
	}

	fake.AssertExists("avatars/me.png")
	fake.AssertContent("avatars/me.png", []byte("png"))
}
```

## 文档

- [文档索引](docs/readme.md)
- [快速入门](docs/quick-start.md)
- [配置](docs/configuration.md)
- [部署](docs/deployment.md)
- [驱动](docs/drivers.md)
- [测试](docs/testing.md)
- [设计与路径安全](docs/design.md)

## 当前范围

已实现：local、S3-compatible、OSS、scoped disk、read-only disk、fake、合约测试、URL 生成、临时下载 URL、provider 支持时的 visibility、copy/move fallback、路径规范化。

暂未实现：multipart upload、provider 原生 batch delete、自定义对象 metadata/header 写入选项、storage class/tagging/encryption、presigned upload URL、SFTP/COS/Qiniu/WebDAV 驱动、CI workflow。
