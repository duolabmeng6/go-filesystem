# 配置 WebDAV

WebDAV 驱动可连接坚果云等兼容 WebDAV 的服务。先在服务提供商处获取 WebDAV 地址及应用密码，再配置储存器：

```go
import (
    "context"
    "os"

    "github.com/duolabmeng6/go-filesystem"
    webdavdriver "github.com/duolabmeng6/go-filesystem/drivers/webdav"
)

ctx := context.Background()
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
    Default: "cloud",
    Disks: map[string]filesystem.DiskConfig{
        "cloud": {
            Driver: "webdav",
            Options: map[string]any{
                "url":      os.Getenv("WEBDAV_URL"),
                "username": os.Getenv("WEBDAV_USERNAME"),
                "password": os.Getenv("WEBDAV_PASSWORD"),
            },
        },
    },
}, filesystem.WithDriver("webdav", webdavdriver.NewFactory()))
if err != nil {
    return err
}

disk := manager.MustDisk("cloud")
if err := disk.Put(ctx, "hello.txt", []byte("你好")); err != nil {
    return err
}
```

URL 使用 HTTP 或 HTTPS，不包含凭据、查询参数或片段；建议使用 HTTPS。连接地址的路径作为储存根目录，用户名和密码单独配置。

为兼容不支持 MOVE 覆盖的服务，覆盖文件时使用 WebDAV PUT 直接写入，因此不能保证原子覆盖。大文件上传会先写入本机临时文件，需要预留相应磁盘空间。
