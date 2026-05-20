# 驱动

驱动实现 `filesystem.Adapter`。业务代码通常只使用 `filesystem.Disk`，不直接依赖 adapter。

## Local 驱动

包：

```go
import "github.com/duolabmeng6/go-filesystem/local"
```

创建 disk：

```go
disk, err := local.NewDisk(local.Config{
	Root:       "storage/app",
	BaseURL:    "/storage",
	Visibility: filesystem.VisibilityPrivate,
})
```

能力：

- 真实本地目录。
- 默认原子写入：写临时文件、chmod、rename。
- `WithOverwrite(false)` 在目标存在时返回 `ErrAlreadyExists`。
- 基于文件权限的 visibility。
- 配置 `BaseURL` 后支持公开 URL。
- 配置 `TemporaryURLBuilder` 后支持临时 URL。
- root 下 symlink 的 best-effort 防护。

限制：

- symlink 防护是应用层保护，不是强沙箱。
- `WithOverwrite(false)` 不是跨进程锁。
- 文件只存在于本机或共享 volume 上。

## S3 驱动

包：

```go
import s3driver "github.com/duolabmeng6/go-filesystem/drivers/s3"
```

创建 disk：

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

能力：

- 对象写入、读取、删除、head、copy、list。
- copy 使用 provider 的 `CopyObject`。
- move 由高层 disk 执行 copy 后 delete。
- 临时下载 URL 使用 AWS SDK presign。
- URL 使用 `BaseURL` 拼接。
- visibility 使用对象 ACL；`DisableACL` 为 true 时禁用。
- `WithOverwrite(false)` 映射为 `IfNoneMatch: "*"`。
- 目录语义是 prefix-only。

S3-compatible 云服务商建议：

| 云服务商类型 | 推荐设置 |
| --- | --- |
| AWS S3 | `Region` 设置为 bucket region；`UsePathStyle` 通常 false；是否禁用 ACL 取决于 bucket ownership 和 ACL 策略。 |
| Cloudflare R2 | `Region: "auto"`，自定义 `Endpoint`，通常 `UsePathStyle: true`，`DisableACL: true`。 |
| Backblaze B2 | 自定义 `Endpoint`，通常 `UsePathStyle: true`，`DisableACL: true`。 |
| MinIO | 自定义 `Endpoint`，通常 `UsePathStyle: true`；`DisableSSL` 只建议本地 HTTP 开发使用。 |

当 `DisableACL` 为 true 时，public/private 访问应由 bucket policy、CDN 或应用逻辑控制，不要调用 `SetVisibility`。

## Aliyun OSS 驱动

包：

```go
import ossdriver "github.com/duolabmeng6/go-filesystem/drivers/oss"
```

创建 disk：

```go
disk, err := ossdriver.NewDisk(ossdriver.Config{
	Bucket:          "my-bucket",
	Region:          "cn-hangzhou",
	Endpoint:        "oss-cn-hangzhou.aliyuncs.com",
	AccessKeyID:     os.Getenv("OSS_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("OSS_ACCESS_KEY_SECRET"),
	SecurityToken:   os.Getenv("OSS_SECURITY_TOKEN"),
	BaseURL:         "https://cdn.example.com",
})
```

能力：

- 对象写入、读取、删除、head、copy、list。
- copy 使用 OSS `CopyObject`。
- move 由高层 disk 执行 copy 后 delete。
- 临时下载 URL 使用 OSS presign。
- URL 使用 `BaseURL` 拼接。
- visibility 使用对象 ACL。
- 目录语义是 prefix-only。

OSS 需要 `Region`、`Endpoint` 或自定义 client。

## 驱动工厂

Factory 让 manager 可以根据 `DiskConfig` 懒加载构建 disk：

```go
manager := filesystem.New(filesystem.WithDefaultDisk("local"))
manager.MustExtend("local", local.NewFactory())
manager.MustExtend("s3", s3driver.NewFactory())
manager.MustExtend("oss", ossdriver.NewFactory())
```

然后配置命名 disk：

```go
err := manager.ConfigureDisk("public", filesystem.DiskConfig{
	Driver:  "local",
	Root:    "storage/app/public",
	BaseURL: "/storage",
})
```

## 自定义驱动

实现核心接口：

```go
type Adapter interface {
	Write(ctx context.Context, path string, r io.Reader, opts filesystem.WriteOptions) error
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	Delete(ctx context.Context, path string) error
	Exists(ctx context.Context, path string) (bool, error)
	Stat(ctx context.Context, path string) (filesystem.FileInfo, error)
	ListPage(ctx context.Context, prefix string, opts filesystem.ListOptions) (filesystem.Page, error)
	Capabilities() filesystem.CapabilitySet
}
```

后端支持时，再实现可选接口：

| 接口 | 用途 |
| --- | --- |
| `Copier` | 原生复制。 |
| `Mover` | 原生移动。 |
| `DirectoryManager` | 真实目录操作。 |
| `DirectorySemanticsProvider` | 声明真实目录、prefix-only 或不支持目录的语义。 |
| `URLGenerator` | 公开 URL 生成。 |
| `TemporaryURLGenerator` | 临时下载 URL 生成。 |
| `VisibilityController` | 获取和设置 public/private visibility。 |

使用 `filesystemtest` 验证自定义驱动：

```go
func TestDriverContracts(t *testing.T) {
	newDisk := func(t testing.TB) *filesystem.Disk {
		adapter, err := mydriver.New(mydriver.Config{Root: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return filesystem.NewDisk(adapter)
	}

	filesystemtest.RunObjectContract(t, newDisk)
	filesystemtest.RunDirectoryContract(t, newDisk)
	filesystemtest.RunListContract(t, newDisk)
	filesystemtest.RunVisibilityContract(t, newDisk)
	filesystemtest.RunPathSafetyContract(t, newDisk)
}
```
