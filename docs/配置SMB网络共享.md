# 配置 SMB 网络共享

使用 SMB 驱动可连接 NAS 或 Windows 共享文件夹。服务器需支持 SMB2/3，认证使用用户名、密码和可选域。

```go
import (
    "context"
    "os"

    "github.com/duolabmeng6/go-filesystem"
    smbdriver "github.com/duolabmeng6/go-filesystem/drivers/smb"
)

ctx := context.Background()
manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
    Default: "nas",
    Disks: map[string]filesystem.DiskConfig{
        "nas": {
            Driver: "smb",
            Root:   "team/docs", // 留空使用共享根目录
            Options: map[string]any{
                "host":     os.Getenv("SMB_HOST"),
                "port":     445,
                "share":    os.Getenv("SMB_SHARE"),
                "username": os.Getenv("SMB_USERNAME"),
                "password": os.Getenv("SMB_PASSWORD"),
                "domain":   os.Getenv("SMB_DOMAIN"), // 可选
            },
        },
    },
}, filesystem.WithDriver("smb", smbdriver.NewFactory()))
if err != nil {
    return err
}

// 固定使用 nas；动态名称请使用 manager.Disk(name) 并处理错误。
disk := manager.MustDisk("nas")
if err := disk.Put(ctx, "hello.txt", []byte("你好")); err != nil {
    return err
}
// 管理器退出或替换储存器时释放共享、会话和 TCP 连接。
if closer, ok := disk.Adapter().(interface{ Close() error }); ok {
    return closer.Close()
}
```

`root` 只能是共享内的相对路径；驱动拒绝 `..`、反斜杠及符号链接，不能通过链接跳出配置目录。驱动不提供对象存储的公开链接或访问权限设置。

覆盖写入使用临时文件后重命名替换。不覆盖写入使用独占创建，避免覆盖同名文件，写入过程中其他客户端可能看到不完整内容。
