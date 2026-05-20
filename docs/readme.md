# go-filesystem 文档

这里是 `github.com/duolabmeng6/go-filesystem` 的使用文档。

这个库的目标是像 Go 标准库一样稳定、直接、可组合：小接口、显式 `context.Context`、流式 I/O、清晰错误、尽量少的全局状态。`Manager` 管理命名 disk，`Disk` 提供业务 API，`Adapter` 负责对接具体存储后端。

## 从这里开始

- [快速入门](quick-start.md)：安装、创建 manager、读写文件、生成 URL。
- [配置](configuration.md)：local、public、S3、OSS、scoped、read-only、强类型配置和环境变量。
- [部署](deployment.md)：本地静态服务、对象存储、CDN、私有下载和生产检查项。
- [驱动](drivers.md)：local、S3-compatible、OSS 的能力矩阵和 provider 注意事项。
- [测试](testing.md)：Fake、PersistentFake、驱动合约测试和真实云存储集成测试。
- [设计](design.md)：API 模型、路径安全、目录语义、错误模型和当前限制。

## 包结构

| 包 | 用途 |
| --- | --- |
| `filesystem` | 核心 manager、disk API、adapter contract、错误、选项、fake、scoped/read-only decorator。 |
| `local` | 本地文件系统 adapter 和 manager factory。 |
| `drivers/s3` | AWS S3 和 S3-compatible adapter。 |
| `drivers/oss` | 阿里云 OSS adapter。 |
| `filesystemtest` | 给 adapter 复用的合约测试。 |

## 能力矩阵

| 能力 | Local | S3 | OSS |
| --- | --- | --- | --- |
| 写入 / 读取 / 删除 | 支持 | 支持 | 支持 |
| 流式上传 / 下载 | 支持 | 支持 | 支持 |
| 复制 | 支持 | 原生对象复制 | 原生对象复制 |
| 移动 | 支持 | copy 后 delete | copy 后 delete |
| 元数据 | size、modified time、MIME | size、modified time、MIME | size、modified time、MIME |
| 平铺 / 递归列表 | 支持 | 支持 | 支持 |
| 分页列表 | 支持 | 支持 | 支持 |
| 目录 | 真实目录 | prefix-only | prefix-only |
| 公开 URL | 配置 `BaseURL` 后支持 | 配置 `BaseURL` 后支持 | 配置 `BaseURL` 后支持 |
| 临时下载 URL | 需要自定义 builder | presigned GET | presigned GET |
| Visibility | 文件权限 | 对象 ACL，除非禁用 | 对象 ACL |
| Scoped 前缀 | 核心 wrapper | 核心 wrapper | 核心 wrapper |
| Read-only | 核心 wrapper | 核心 wrapper | 核心 wrapper |

## 维护说明

- 默认单测和 mock-backed 测试通过 `go test ./...` 执行。
- 真实云存储集成测试由环境变量控制，不建议在公开 CI 默认运行。
- `.env.example` 只是模板，真实密钥应放在本地 `.env`、shell 环境或部署平台的 secret store。
- 文档索引沿用仓库已有的 `docs/readme.md` 小写路径，避免大小写重命名在不同文件系统上引起问题。
