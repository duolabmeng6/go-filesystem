# 设计

`go-filesystem` 的目标是成为一个小而稳定的存储层，让业务代码不直接依赖本地路径、S3 SDK 或 OSS SDK。它避免全局 facade，依赖关系保持显式。

## 核心模型

| 类型 | 职责 |
| --- | --- |
| `Manager` | 管理 disk 配置、命名 disk、默认 disk 和 driver factory。 |
| `Disk` | 业务 API 层，负责路径规范化、fallback 行为、错误包装和可选能力检查。 |
| `Adapter` | 最小后端接口，由 local、S3、OSS 或自定义驱动实现。 |
| 可选接口 | 增加原生 copy、move、directory、URL、temporary URL、visibility 等能力。 |

大多数应用只需要依赖 `*filesystem.Disk` 或 `*filesystem.Manager`。自定义存储后端实现 `filesystem.Adapter`。

## 路径

路径是对象存储风格的 slash path，不是操作系统路径。合法路径示例：

```text
avatars/user-1.png
reports/2026/may.csv
```

核心会拒绝：

- 对象操作中的空路径；
- 绝对路径；
- `.` 和 `..` segment；
- 重复斜杠；
- 反斜杠；
- NUL 和控制字符；
- Windows drive 或 UNC 风格路径；
- 冒号 segment；
- `CON`、`NUL`、`COM1` 等 Windows 保留名。

列表操作允许空 prefix，用来列出根目录。

## 目录语义

本地存储有真实目录。S3 和 OSS 是 prefix-only 目录语义。

这意味着：

- 对象存储上的 `MakeDirectory` 基本是 no-op。
- 对象存储上的 `DeleteDirectory` 会删除 prefix 下的文件。
- 当某个 prefix 有子对象时，即使没有同名对象，`Exists` 和 `Stat` 也可以把它视为目录。
- S3/OSS 不持久化空目录。

## URL 语义

`URL` 只拼接 `BaseURL` 和转义后的路径，不检查 provider policy 或网络可达性。

```go
url, err := disk.URL(ctx, "docs/hello world#1.txt")
```

当 `BaseURL` 是 `https://cdn.example.com/files` 时，结果是：

```text
https://cdn.example.com/files/docs/hello%20world%231.txt
```

## 临时 URL

`TemporaryURL` 要求过期时间在未来。S3 和 OSS 实现 presigned GET URL。本地驱动只有配置 `TemporaryURLBuilder` 后才支持。

URL option 当前主要用于下载响应参数：

```go
url, err := disk.TemporaryURL(
	ctx,
	"reports/a.pdf",
	time.Now().Add(10*time.Minute),
	filesystem.WithURLParameter("response-content-disposition", `attachment; filename="report.pdf"`),
)
```

## Visibility

Visibility 只有两个业务语义：

```go
filesystem.VisibilityPrivate
filesystem.VisibilityPublic
```

后端映射：

| 驱动 | 映射方式 |
| --- | --- |
| Local | 文件和目录权限。 |
| S3 | 对象 ACL，除非 `DisableACL` 为 true。 |
| OSS | 对象 ACL。 |

有些对象存储 provider 不推荐或不支持每对象 ACL。这时应设置 `DisableACL`，公开访问交给 bucket policy、CDN 或应用鉴权。

## Fallback

`Disk` 提供高层 fallback：

- adapter 支持原生 copy 时，`Copy` 使用原生 copy。
- 否则 `Copy` 打开源文件并写入目标。
- `Move` 优先使用原生 move；否则 copy 后 delete。
- prefix-only 目录的 copy/move 会遍历 prefix 下的文件。

如果 move 中 copy 成功但清理源失败，错误会包装 `ErrPartialFailure`。

## 错误

操作错误会包装成 `OpError`，并保留 sentinel 给 `errors.Is` 使用。

```go
if err := disk.Delete(ctx, "missing.txt"); errors.Is(err, filesystem.ErrNotFound) {
	return nil
}
```

批量操作可能返回 `MultiError`，它会 unwrap 子错误。

## 并发

`Manager` 用 mutex 保护配置和 disk cache。具体 disk 操作依赖 adapter 和后端语义。

本地驱动适合正常并发使用，但 `WithOverwrite(false)` 不是跨进程锁。云后端遵循 provider 自己的一致性和并发语义。

## 当前限制

已实现：

- local、S3-compatible、OSS 驱动；
- scoped disk；
- read-only disk；
- fake 和合约测试；
- 公开 URL 生成；
- 临时下载 URL；
- provider 支持时的 visibility；
- 路径规范化和本地 symlink 检查。

暂未实现：

- multipart upload；
- provider 原生 batch delete；
- 自定义对象 metadata/header 写入选项；
- cache-control/content-type 写入选项；
- storage class、tagging、server-side encryption；
- presigned upload URL；
- SFTP、COS、Qiniu、FTP、WebDAV 驱动；
- CI workflow。
