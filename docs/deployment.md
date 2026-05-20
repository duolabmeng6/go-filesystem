# 部署

`go-filesystem` 负责存储和寻址文件，不负责启动 HTTP 服务、创建 bucket、配置 CDN、设置对象存储策略，也不会验证生成的 URL 是否真的可访问。生产部署需要把 disk 和你的 Web 应用、反向代理、对象存储策略、CDN、密钥管理连接起来。

## 本地私有存储

私有本地存储适合报表、发票、导入文件、用户私密附件等只能通过应用鉴权下载的文件。

```go
disk, err := local.NewDisk(local.Config{
	Root:       "/srv/myapp/storage/private",
	Visibility: filesystem.VisibilityPrivate,
})
```

建议：

- 把 storage root 放在静态文件目录之外。
- 容器部署时挂载持久化 volume。
- 文件是业务数据时，给 root 做备份。
- 不要直接信任用户传入的路径；先映射成应用自己控制的路径。
- 下载通过应用 handler 鉴权后调用 `Open`。

下载 handler 示例：

```go
func downloadInvoice(disk *filesystem.Disk) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		path := "invoices/" + chi.URLParam(r, "id") + ".pdf"

		rc, err := disk.Open(ctx, path)
		if errors.Is(err, filesystem.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "download failed", http.StatusInternalServerError)
			return
		}
		defer rc.Close()

		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.Copy(w, rc)
	}
}
```

## 本地公开存储

公开本地存储适合头像、封面图、公开附件等可以直接由 Web server 或框架静态文件中间件服务的文件。

```go
disk, err := local.NewDisk(local.Config{
	Root:       "/srv/myapp/storage/public",
	BaseURL:    "https://app.example.com/storage",
	Visibility: filesystem.VisibilityPublic,
})
```

同时需要配置 Web server。例如 Nginx：

```nginx
location /storage/ {
    alias /srv/myapp/storage/public/;
    try_files $uri =404;
}
```

这时 `disk.URL(ctx, "avatars/me.png")` 返回 `https://app.example.com/storage/avatars/me.png`。

## 对象存储和 CDN

S3 或 OSS 适合作为多实例共享的持久化后端，通常再接 CDN 或自定义域名。

```go
disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          os.Getenv("S3_BUCKET"),
	Region:          os.Getenv("S3_REGION"),
	Endpoint:        os.Getenv("S3_ENDPOINT"),
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	BaseURL:         "https://cdn.example.com",
	UsePathStyle:    true,
	DisableACL:      true,
})
```

部署职责：

- bucket 创建、生命周期、复制、加密是 provider 层配置。
- 公开访问通常通过 bucket policy、CDN origin 规则或 CDN signed URL 管理。
- `BaseURL` 要配置成客户端真正可以访问的域名。
- 密钥来自 secret manager、环境变量或云平台身份，不要写进代码。
- R2/B2 这类 provider 建议 `DisableACL: true`，不要依赖对象 ACL 管访问。

## 私有下载

provider 支持签名时，使用短期 `TemporaryURL`：

```go
url, err := disk.TemporaryURL(
	r.Context(),
	"invoices/a.pdf",
	time.Now().Add(15*time.Minute),
	filesystem.WithURLParameter("response-content-disposition", `attachment; filename="invoice.pdf"`),
)
```

本地 disk 需要提供符合你路由和签名方案的 `TemporaryURLBuilder`：

```go
disk, err := local.NewDisk(local.Config{
	Root: "/srv/myapp/storage/private",
	TemporaryURLBuilder: func(ctx context.Context, path string, expiresAt time.Time, opts filesystem.URLOptions) (string, error) {
		return signApplicationDownloadURL(path, expiresAt, opts.Parameters), nil
	},
})
```

## 容器部署

本地驱动检查项：

- 将 `/srv/myapp/storage` 或你的 root 挂载为持久化 volume。
- 确保应用运行用户有读写权限。
- 多进程或多实例共享同一个本地 root 时，需要明确处理文件竞争。
- 多实例共享文件时，更推荐对象存储。

对象存储检查项：

- 每个环境使用独立 bucket 或 prefix。
- 使用最小权限密钥。
- 通过应用 `context` 设置超时。
- 给临时文件和测试 prefix 配置生命周期清理。

## 多租户前缀

使用 `PathPrefix` 或 `filesystem.Scoped`，让租户路径隔离内建在 disk 中：

```go
tenantDisk, err := manager.Build(ctx, filesystem.DiskConfig{
	Driver:     "s3",
	PathPrefix: "tenants/" + tenantID,
	Options: map[string]any{
		"bucket": os.Getenv("S3_BUCKET"),
		"region": os.Getenv("S3_REGION"),
	},
})
```

`tenantID` 仍然应该由业务层校验。路径规范化会拒绝危险路径语法，但租户 ID 应该是应用自己控制的标识。

## 生产检查清单

- 发布前运行 `go test ./...`。
- 确认 `BaseURL` 指向真实可访问的路由、CDN 或 bucket 域名。
- 确认 bucket policy / CDN 规则符合 public/private 模型。
- 默认使用 private visibility，需要公开时显式 opt-in。
- 不允许覆盖时使用 `WithOverwrite(false)`。
- 用 `errors.Is` 处理 `ErrNotFound`、`ErrAlreadyExists`、`ErrUnsupported`、`ErrReadOnly` 和 context cancellation。
- 网络后端应给请求 context 设置 deadline 或 timeout。
