# 配置

`go-filesystem` 支持两种配置方式：

- 使用 `filesystem.Config` / `filesystem.DiskConfig` 配置 `Manager`，适合从环境变量或配置文件加载多个 disk。
- 使用驱动包的强类型配置直接创建 `Disk`，适合纯 Go 代码里构造单个存储后端。

## Manager 配置

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
```

也可以逐步注册驱动和配置 disk：

```go
manager := filesystem.New(filesystem.WithDefaultDisk("s3"))
manager.MustExtend("local", local.NewFactory())
manager.MustExtend("s3", s3driver.NewFactory())

err := manager.ConfigureDisk("s3", filesystem.DiskConfig{
	Driver: "s3",
	Options: map[string]any{
		"bucket":            os.Getenv("S3_BUCKET"),
		"region":            os.Getenv("S3_REGION"),
		"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
	},
})
```

## DiskConfig 字段

| 字段 | 说明 |
| --- | --- |
| `Driver` | 已注册的驱动名，例如 `local`、`s3`、`oss`。 |
| `Root` | 本地根目录；S3/OSS factory 中也可作为 bucket fallback。 |
| `BaseURL` | `URL` 使用的公开 URL 前缀；不会自动服务文件。 |
| `Visibility` | 默认 visibility：`filesystem.VisibilityPrivate` 或 `filesystem.VisibilityPublic`。 |
| `PathPrefix` | 由核心 `Scoped` wrapper 应用的路径前缀，适合租户或应用命名空间。 |
| `ReadOnly` | 用 `ReadOnly` wrapper 包装 adapter，禁止变更操作。 |
| `Options` | 驱动特定参数，例如 bucket、region、endpoint、credentials。 |

## 本地磁盘

```go
disk, err := local.NewDisk(local.Config{
	Root:       "storage/app/public",
	BaseURL:    "/storage",
	Visibility: filesystem.VisibilityPublic,
})
```

默认值：

| 字段 | 默认值 |
| --- | --- |
| `Root` | `./storage` |
| `Visibility` | `private` |
| public file mode | `0644` |
| private file mode | `0600` |
| public directory mode | `0755` |
| private directory mode | `0700` |

自定义权限：

```go
disk, err := local.NewDisk(local.Config{
	Root: "storage/app",
	Permissions: local.Permissions{
		FilePublic:  0o640,
		FilePrivate: 0o600,
		DirPublic:   0o750,
		DirPrivate:  0o700,
	},
})
```

本地临时 URL 需要自定义 builder：

```go
disk, err := local.NewDisk(local.Config{
	Root: "/srv/private",
	TemporaryURLBuilder: func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
		return signDownloadURL(path, expiresAt), nil
	},
})
```

## S3 和 S3-compatible

强类型配置：

```go
disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          os.Getenv("S3_BUCKET"),
	Region:          os.Getenv("S3_REGION"),
	Endpoint:        os.Getenv("S3_ENDPOINT"),
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	SessionToken:    os.Getenv("S3_SESSION_TOKEN"),
	BaseURL:         os.Getenv("S3_BASE_URL"),
	UsePathStyle:    true,
	DisableACL:      true,
	Visibility:      filesystem.VisibilityPrivate,
})
```

Manager 配置：

```go
manager.MustExtend("s3", s3driver.NewFactory())

err := manager.ConfigureDisk("s3", filesystem.DiskConfig{
	Driver:  "s3",
	BaseURL: os.Getenv("S3_BASE_URL"),
	Options: map[string]any{
		"bucket":            os.Getenv("S3_BUCKET"),
		"region":            os.Getenv("S3_REGION"),
		"endpoint":          os.Getenv("S3_ENDPOINT"),
		"access_key_id":     os.Getenv("S3_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("S3_ACCESS_KEY_SECRET"),
		"session_token":     os.Getenv("S3_SESSION_TOKEN"),
		"use_path_style":    true,
		"disable_acl":       true,
	},
})
```

S3 配置说明：

| 字段 | 说明 |
| --- | --- |
| `Bucket` | 必填，除非用 `DiskConfig.Root` 提供。 |
| `Region` | 默认 `us-east-1`；R2 常用 `auto`。 |
| `Endpoint` | S3-compatible 服务的自定义 endpoint。 |
| `UsePathStyle` | 很多 S3-compatible 服务需要打开。 |
| `DisableACL` | R2、B2 通常应打开。公开访问交给 bucket policy、CDN 或应用鉴权。 |
| `DisableSSL` | endpoint 没有 scheme 时使用 `http://`；生产环境优先 TLS。 |

当 `DisableACL` 为 true 时，`VisibilityPublic` 不支持，`GetVisibility` / `SetVisibility` 会返回 `ErrUnsupported`。

## 阿里云 OSS

强类型配置：

```go
disk, err := ossdriver.NewDisk(ossdriver.Config{
	Bucket:          os.Getenv("OSS_BUCKET"),
	Region:          os.Getenv("OSS_REGION"),
	Endpoint:        os.Getenv("OSS_ENDPOINT"),
	AccessKeyID:     os.Getenv("OSS_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("OSS_ACCESS_KEY_SECRET"),
	SecurityToken:   os.Getenv("OSS_SECURITY_TOKEN"),
	BaseURL:         os.Getenv("OSS_BASE_URL"),
	Visibility:      filesystem.VisibilityPrivate,
})
```

Manager 配置：

```go
manager.MustExtend("oss", ossdriver.NewFactory())

err := manager.ConfigureDisk("oss", filesystem.DiskConfig{
	Driver:  "oss",
	BaseURL: os.Getenv("OSS_BASE_URL"),
	Options: map[string]any{
		"bucket":            os.Getenv("OSS_BUCKET"),
		"region":            os.Getenv("OSS_REGION"),
		"endpoint":          os.Getenv("OSS_ENDPOINT"),
		"access_key_id":     os.Getenv("OSS_ACCESS_KEY_ID"),
		"access_key_secret": os.Getenv("OSS_ACCESS_KEY_SECRET"),
		"security_token":    os.Getenv("OSS_SECURITY_TOKEN"),
		"use_cname":         false,
	},
})
```

OSS 需要提供 `Region`、`Endpoint` 或自定义 client。

## Scoped Disk

配置命名 disk 时使用 `PathPrefix`：

```go
err := manager.ConfigureDisk("tenant", filesystem.DiskConfig{
	Driver:     "s3",
	PathPrefix: "tenants/acme",
	Options: map[string]any{
		"bucket": os.Getenv("S3_BUCKET"),
		"region": os.Getenv("S3_REGION"),
	},
})
```

也可以直接包装 adapter：

```go
base, err := local.New(local.Config{Root: "storage/app"})
if err != nil {
	return err
}

adapter, err := filesystem.Scoped(base, "tenants/acme")
if err != nil {
	return err
}
tenantDisk := filesystem.NewDisk(adapter)
```

业务代码看到的是 `avatar.png`，底层后端收到的是 `tenants/acme/avatar.png`。

## Read-only Disk

```go
err := manager.ConfigureDisk("archive", filesystem.DiskConfig{
	Driver:   "s3",
	ReadOnly: true,
	Options: map[string]any{
		"bucket": os.Getenv("ARCHIVE_BUCKET"),
		"region": os.Getenv("S3_REGION"),
	},
})
```

只读 disk 保留读取、列表、URL、临时 URL 和 visibility 读取能力。写入、删除、复制、移动、目录变更和 visibility 修改会返回 `ErrReadOnly`。

## 环境变量模板

仓库包含 `.env.example`，用于集成测试和本地开发。真实密钥不要提交到 git：

```env
S3_TEST_BUCKET=
S3_TEST_REGION=
S3_TEST_ENDPOINT=
S3_TEST_BASE_URL=
S3_TEST_PREFIX=
S3_TEST_USE_PATH_STYLE=
S3_TEST_SKIP_VISIBILITY=
S3_TEST_DISABLE_ACL=
S3_ACCESS_KEY_ID=
S3_ACCESS_KEY_SECRET=
S3_SESSION_TOKEN=
```

库本身不会自动读取 `.env`，应用可以使用自己的配置加载方案。
