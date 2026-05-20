# 快速入门

本教程搭建一个最小可用配置：一个私有本地 disk 和一个公开本地 disk。多数应用最终都需要命名 disk、默认 disk 和测试替换能力，所以推荐从 `Manager` 开始。

## 1. 安装

```sh
go get github.com/duolabmeng6/go-filesystem
```

## 2. 创建 Manager

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
}
```

`manager.Put` 写入默认 disk。需要指定存储后端时，使用 `manager.Disk(name)` 获取命名 disk。

## 3. 写入和读取

```go
ctx := context.Background()

if err := manager.Put(ctx, "reports/hello.txt", []byte("hello")); err != nil {
	return err
}

content, err := manager.Get(ctx, "reports/hello.txt")
if err != nil {
	return err
}
_ = content
```

小文件用 `Put`，大文件或上传流用 `Write`：

```go
file, err := os.Open("large-video.mp4")
if err != nil {
	return err
}
defer file.Close()

err = manager.Write(ctx, "videos/large-video.mp4", file)
```

## 4. 使用公开 Disk

```go
publicDisk, err := manager.Disk("public")
if err != nil {
	return err
}

err = publicDisk.Put(
	ctx,
	"avatars/me.png",
	data,
	filesystem.WithVisibility(filesystem.VisibilityPublic),
)
if err != nil {
	return err
}

url, err := publicDisk.URL(ctx, "avatars/me.png")
if err != nil {
	return err
}
```

当 `BaseURL` 是 `/storage` 时，URL 是 `/storage/avatars/me.png`。路径会按 segment 转义，例如 `docs/hello world#1.txt` 会变成 `/storage/docs/hello%20world%231.txt`。

## 5. 列表

```go
files, err := publicDisk.Files(ctx, "avatars")
allFiles, err := publicDisk.AllFiles(ctx, "")

page, err := publicDisk.ListPage(ctx, "", filesystem.WithPageSize(100))
nextPage, err := publicDisk.ListPage(ctx, "", filesystem.WithPageSize(100), filesystem.WithCursor(page.NextCursor))
```

`Files` 是非递归列表，`AllFiles` 是递归列表。目录或 bucket 较大时，优先使用 `ListPage` 或 `List`。

## 6. 复制、移动、删除

```go
if err := publicDisk.Copy(ctx, "avatars/me.png", "avatars/me-copy.png"); err != nil {
	return err
}

if err := publicDisk.Move(ctx, "avatars/me-copy.png", "archive/me.png"); err != nil {
	return err
}

if err := publicDisk.DeleteIfExists(ctx, "archive/me.png"); err != nil {
	return err
}
```

对象存储驱动有原生 copy 时会使用原生能力。move 通常是 copy 后 delete。

## 7. 临时 URL

S3 和 OSS 支持 presigned 下载 URL：

```go
url, err := disk.TemporaryURL(ctx, "invoices/a.pdf", time.Now().Add(15*time.Minute))
```

本地驱动需要业务方提供签名 URL builder，因为本地下载路由和签名方式取决于你的 HTTP 框架：

```go
disk, err := local.NewDisk(local.Config{
	Root: "/srv/private",
	TemporaryURLBuilder: func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
		return signDownloadURL(path, expiresAt), nil
	},
})
```

## 8. 测试 Fake

测试中导入 `local` 包，让它注册本地 fake factory。

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

`Fake` 会在 `t.Cleanup` 中恢复原 disk。
