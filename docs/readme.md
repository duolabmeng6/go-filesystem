# go-filesystem 实现概览

本文档记录当前代码库已经落地的文件系统抽象、驱动能力、云存储接入方式、测试状态，以及尚未实现的范围。真实访问密钥只应保存在本地 `.env`，不要提交到仓库。

## 状态标记

- `已实现`：代码已落地，并有单测、合约测试或真实环境验证。
- `部分实现`：核心能力可用，但存在 provider 差异、可选配置或明确限制。
- `未实现`：当前代码库没有实现。
- `待完善`：已有基础能力，但还需要补文档、CI、示例或更完整的生产能力。

## 总览

| 模块 | 状态 | 说明 |
| --- | --- | --- |
| 核心抽象 | 已实现 | `Adapter`、`Disk`、`Manager`、`DiskConfig`、能力集、错误类型、路径规范化。 |
| 本地驱动 | 已实现 | 位于 `local`，支持文件、目录、列表、URL、visibility、可选临时 URL。 |
| 阿里云 OSS 驱动 | 已实现 | 位于 `drivers/oss`，已接入真实 OSS 环境测试。 |
| S3 驱动 | 已实现 | 位于 `drivers/s3`，支持 AWS S3 SDK v2 和 S3-compatible endpoint。 |
| Backblaze B2 | 已实现 | 通过 `S3_*` 环境变量实测，ACL/visibility 禁用。 |
| Cloudflare R2 | 已实现 | 通过 `R2_*` 环境变量实测，ACL/visibility 禁用。 |
| Fake / 测试替身 | 已实现 | `Fake`、`PersistentFake`、断言工具。 |
| 驱动合约测试 | 已实现 | `filesystemtest` 覆盖对象操作、列表、visibility、路径安全、URL、临时 URL。 |
| CI 配置 | 未实现 | 当前没有自动化 CI 文件。 |
| `.env.example` | 未实现 | 文档里有模板，但还没有单独示例文件。 |
| SFTP / COS / Qiniu 等更多驱动 | 未实现 | 当前只实现 local、OSS、S3。 |

## 核心能力

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 多 disk 管理 | 已实现 | `Manager` 支持默认 disk、命名 disk、动态注册 driver、配置 disk。 |
| 读写文件 | 已实现 | `Put`、`Write`、`Get`、`Open`，支持 `context.Context` 和 streaming I/O。 |
| 删除文件 | 已实现 | `Delete`、`DeleteIfExists`、`DeleteMany`。 |
| 存在性检查 | 已实现 | `Exists`、`Missing`。 |
| 文件元数据 | 已实现 | `Stat`、`Size`、`LastModified`、`MimeType`。 |
| 复制和移动 | 已实现 | 驱动支持原生 copy 时使用原生能力，否则走读写 fallback；move 通过 copy + delete fallback。 |
| 列表和分页 | 已实现 | `List`、`ListPage`、`Files`、`AllFiles`、`Directories`、`AllDirectories`。 |
| 目录操作 | 部分实现 | 本地驱动是真实目录；对象存储使用 prefix-only 目录语义。 |
| URL 拼接 | 已实现 | `URL` 使用 `BaseURL` 拼接，并按 path segment 转义。 |
| 临时 URL | 部分实现 | OSS/S3 已实现 presign；本地驱动需要业务方提供 builder。 |
| visibility | 部分实现 | 本地、OSS、标准 S3 ACL 可用；B2/R2 通过 `DisableACL` 禁用。 |
| 路径安全 | 已实现 | 统一拒绝危险路径、绝对路径、Windows 保留名、反斜杠、NUL/control 字符等。 |
| scoped disk | 已实现 | `filesystem.Scoped` 对底层 adapter 自动加前缀，并在列表时去掉前缀。 |
| read-only disk | 已实现 | `filesystem.ReadOnly` 拦截写入、删除、移动、目录变更、visibility 变更。 |
| fake disk | 已实现 | 支持测试替换和 `t.Cleanup` 自动恢复。 |
| 原生批量删除 | 未实现 | `DeleteMany` 目前是高层循环删除，不调用 OSS/S3 batch delete API。 |
| 分片上传 / multipart upload | 未实现 | 当前写入走单对象上传。 |
| 自定义对象 metadata / Content-Type 写入 | 未实现 | 当前没有暴露写入 metadata、cache-control、content-type 的统一 option。 |
| 服务端加密、tagging、storage class | 未实现 | 当前没有暴露这些云厂商高级参数。 |
| presigned upload URL | 未实现 | 当前只提供下载用的临时 URL。 |

## 本地驱动

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 文件写入、读取、删除 | 已实现 | 支持 `Put`、`Write`、`Open`、`Get`、`Delete`。 |
| 目录创建和删除 | 已实现 | 使用真实本地目录。 |
| 列表和分页 | 已实现 | 支持递归和非递归列表。 |
| visibility | 已实现 | 映射为本地文件权限。 |
| URL | 已实现 | 通过 `BaseURL` 拼接。 |
| 临时 URL | 部分实现 | 需要配置 `TemporaryURLBuilder`。 |
| 原子写入 | 已实现 | 默认先写临时文件，再 rename。 |
| symlink 防护 | 部分实现 | 应用层 best-effort 检查，不是强沙箱。 |
| 跨进程强锁 | 未实现 | `WithOverwrite(false)` 不是跨进程锁。 |

## OSS 驱动

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 对象写入、读取、删除 | 已实现 | 使用 OSS SDK v2。 |
| 对象复制 | 已实现 | 使用 `CopyObject`。 |
| 对象移动 | 已实现 | 高层 copy + delete。 |
| 元数据读取 | 已实现 | `HeadObject` 获取 size、last modified、mime type。 |
| 列表和分页 | 已实现 | 使用 `ListObjectsV2`，支持递归和 delimiter。 |
| URL | 已实现 | 通过 `BaseURL` 拼接。 |
| 临时 URL | 已实现 | 使用 OSS SDK presign。 |
| visibility | 已实现 | 使用对象 ACL。 |
| PathPrefix | 已实现 | 通过 `filesystem.Scoped` 实现。 |
| 真实 OSS 集成测试 | 已实现 | `.env` 中 `OSS_*` 配置可运行真实环境合约。 |
| 分片上传 | 未实现 | 还没有 multipart upload。 |
| batch delete | 未实现 | 目录删除和多文件删除当前走循环删除。 |
| 自定义 metadata / headers | 未实现 | 还没有统一写入选项。 |

OSS 常用配置：

```go
disk, err := ossdriver.NewDisk(ossdriver.Config{
	Bucket:          "my-bucket",
	Region:          "cn-hongkong",
	Endpoint:        "oss-cn-hongkong.aliyuncs.com",
	AccessKeyID:     os.Getenv("OSS_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("OSS_ACCESS_KEY_SECRET"),
	SecurityToken:   os.Getenv("OSS_SECURITY_TOKEN"),
	BaseURL:         "https://cdn.example.com",
	Visibility:      filesystem.VisibilityPrivate,
})
```

## S3 驱动

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| 对象写入、读取、删除 | 已实现 | 使用 AWS SDK for Go v2。 |
| 对象复制 | 已实现 | 使用 `CopyObject`。 |
| 对象移动 | 已实现 | 高层 copy + delete。 |
| 元数据读取 | 已实现 | `HeadObject` 获取 size、last modified、mime type。 |
| 列表和分页 | 已实现 | 使用 `ListObjectsV2`，支持递归和 delimiter。 |
| URL | 已实现 | 通过 `BaseURL` 拼接。 |
| 临时 URL | 已实现 | 使用 AWS SDK presign。 |
| visibility | 部分实现 | 标准 S3 ACL 可用；B2/R2 这类不支持 ACL 的服务需禁用。 |
| `WithOverwrite(false)` | 已实现 | 映射为 `IfNoneMatch: "*"`。 |
| 自定义 endpoint | 已实现 | 支持 S3-compatible endpoint。 |
| path-style | 已实现 | `UsePathStyle` 支持 B2/R2 等服务。 |
| `DisableACL` | 已实现 | 禁用对象 ACL 写入，并移除 visibility 能力。 |
| session token | 已实现 | 支持临时凭据。 |
| Backblaze B2 真实测试 | 已实现 | 通过 `S3_*` 环境变量测试。 |
| Cloudflare R2 真实测试 | 已实现 | 通过 `R2_*` 环境变量测试。 |
| AWS 官方 S3 真实测试 | 待完善 | 驱动兼容 AWS S3，但当前本地只验证了 mock、B2、R2。 |
| 分片上传 | 未实现 | 还没有 multipart upload。 |
| batch delete | 未实现 | 目录删除和多文件删除当前走循环删除。 |
| 自定义 metadata / headers | 未实现 | 还没有统一写入选项。 |
| presigned upload URL | 未实现 | 当前只支持下载临时 URL。 |

S3 常用配置：

```go
disk, err := s3driver.NewDisk(context.Background(), s3driver.Config{
	Bucket:          "my-bucket",
	Region:          "us-east-1",
	Endpoint:        "https://s3.example.com",
	AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
	AccessKeySecret: os.Getenv("S3_ACCESS_KEY_SECRET"),
	SessionToken:    os.Getenv("S3_SESSION_TOKEN"),
	BaseURL:         "https://files.example.com",
	UsePathStyle:    true,
	DisableACL:      true,
	Visibility:      filesystem.VisibilityPrivate,
})
```

## S3-compatible 实测环境

| 服务 | 状态 | 环境变量前缀 | 说明 |
| --- | --- | --- | --- |
| Backblaze B2 | 已实现 | `S3_` | 已验证读写、删除、复制、移动、列表、URL、临时 URL。 |
| Cloudflare R2 | 已实现 | `R2_` | 已验证读写、删除、复制、移动、列表、URL、临时 URL。 |
| B2/R2 对象 ACL | 未实现 | `S3_TEST_DISABLE_ACL=true` / `R2_TEST_DISABLE_ACL=true` | provider 不适合使用对象 ACL，当前显式禁用。 |
| B2/R2 visibility | 未实现 | `S3_TEST_SKIP_VISIBILITY=true` / `R2_TEST_SKIP_VISIBILITY=true` | public/private 应由 bucket 策略、CDN、应用层鉴权处理。 |
| 自定义域名 URL | 已实现 | `S3_TEST_BASE_URL` / `R2_TEST_BASE_URL` | 已验证 `https://file1.rongyiapi.com` 和 `https://r2.rongyiapi.com` 可访问测试对象。 |

`.env` 示例，不包含真实密钥：

```env
S3_TEST_BUCKET=
S3_TEST_REGION=
S3_TEST_ENDPOINT=
S3_TEST_BASE_URL=
S3_TEST_PREFIX=go-filesystem-tests
S3_TEST_USE_PATH_STYLE=true
S3_TEST_SKIP_VISIBILITY=true
S3_TEST_DISABLE_ACL=true
S3_ACCESS_KEY_ID=
S3_ACCESS_KEY_SECRET=
S3_SESSION_TOKEN=

R2_TEST_BUCKET=
R2_TEST_REGION=auto
R2_TEST_ENDPOINT=
R2_TEST_BASE_URL=
R2_TEST_PREFIX=go-filesystem-tests
R2_TEST_USE_PATH_STYLE=true
R2_TEST_SKIP_VISIBILITY=true
R2_TEST_DISABLE_ACL=true
R2_ACCESS_KEY_ID=
R2_ACCESS_KEY_SECRET=
R2_SESSION_TOKEN=
```

## URL 与临时 URL

| 能力 | 状态 | 说明 |
| --- | --- | --- |
| URL 拼接 | 已实现 | 使用 `BaseURL + "/" + escaped path`。 |
| scoped URL | 已实现 | 使用 `PathPrefix` 或 `Scoped` 时，URL 中会包含 prefix。 |
| path segment escape | 已实现 | 空格、`#`、`?` 等字符会正确转义。 |
| 自定义域名可访问性验证 | 部分实现 | 测试中人工用 `curl` 验证过当前 B2/R2 域名；库本身不发 HTTP 请求验证。 |
| OSS 下载临时 URL | 已实现 | 使用 OSS SDK presign。 |
| S3 下载临时 URL | 已实现 | 使用 AWS SDK presign。 |
| 本地下载临时 URL | 部分实现 | 需要业务方传入 builder。 |
| 上传临时 URL | 未实现 | 目前没有 presigned PUT/POST。 |

## 测试状态

| 测试 | 状态 | 命令 |
| --- | --- | --- |
| 全量单测和集成测试 | 已实现 | `go test ./...` |
| OSS 真实环境合约 | 已实现 | `go test ./drivers/oss -run TestIntegrationContracts -count=1 -v` |
| B2/S3-compatible 真实环境合约 | 已实现 | `go test ./drivers/s3 -run TestIntegrationContracts -count=1 -v` |
| R2 真实环境合约 | 已实现 | `go test ./drivers/s3 -run TestR2IntegrationContracts -count=1 -v` |
| race 测试 | 待完善 | 之前可以手动运行；尚未加入 CI。 |
| CI 自动测试 | 未实现 | 仓库当前没有 CI 配置。 |
| 自动真实云存储集成测试 | 未实现 | 不建议默认 CI 跑真实云存储，需要手动或受控环境触发。 |

## 未实现和待完善清单

| 项目 | 状态 | 建议 |
| --- | --- | --- |
| `.env.example` | 未实现 | 增加 OSS、S3、R2 的空模板，避免污染真实 `.env`。 |
| CI | 未实现 | 先跑 `go test ./...` 的 mock/unit 部分，真实云存储测试手动触发。 |
| SFTP 驱动 | 未实现 | 新增独立 driver 包并接入 `filesystemtest` 合约。 |
| 腾讯 COS 驱动 | 未实现 | 可按 OSS/S3 driver 结构实现。 |
| 七牛 Kodo 驱动 | 未实现 | 可按独立 driver 包实现。 |
| FTP/WebDAV 驱动 | 未实现 | 当前没有实现计划。 |
| multipart upload | 未实现 | 对大文件上传性能和稳定性有价值。 |
| batch delete | 未实现 | 对批量删除和目录删除性能有价值。 |
| 对象 metadata/header options | 未实现 | 需要设计统一 `WriteOption`，覆盖 content-type、cache-control、metadata 等。 |
| presigned upload URL | 未实现 | 适合前端直传场景。 |
| provider 公开策略管理 | 未实现 | 不建议作为文件抽象核心能力，公开访问更适合由 bucket/CDN/应用层配置。 |
| 完整示例项目 | 待完善 | 可以补一个 `examples/`，展示 local、OSS、S3、R2 配置。 |

## 当前限制

- `URL` 不验证域名是否真实可访问，只负责拼接；是否能访问由 bucket 公开策略、CDN、反向代理或自定义域名配置决定。
- B2/R2 不启用对象 ACL，因此 visibility 能力在这两个环境中标记为未实现。
- 对象存储目录是 prefix-only 语义，不会写入空目录 marker。
- 本地驱动的 symlink 防护是应用层 best-effort，不是强沙箱。
- 本地 `WithOverwrite(false)` 不是跨进程强锁。
