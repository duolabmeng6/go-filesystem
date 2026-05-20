# go-filesystem Agent Notes

进入本仓库后先阅读此文件。用户主要面向中文使用者，维护和发版时按这里执行。

## 文档约定

- README 和 `docs/` 必须使用中文。
- `docs/` 文档文件名也使用中文，例如 `快速入门.md`、`配置对象存储.md`。
- 文档优先写“怎么使用这个包”，少解释内部结构、实现细节、能力矩阵或设计理念。
- README 保持简短：Go Reference badge、pkg.go.dev 地址、安装、最小示例、常见用法、中文教程入口。
- GoDoc 包注释可以中文，且第一段要直接告诉用户怎么开始用，不要写成架构说明。
- 固定写入某个已配置 disk 的示例，优先写 `manager.MustDisk("local").Put(...)`、`manager.MustDisk("s3").Put(...)`、`manager.MustDisk("oss").Put(...)`，让用户一眼看到可以按名称切换存储。
- disk 名称来自命令行、环境变量、租户配置或后台表单时，示例必须使用 `manager.Disk(name)` 并处理 error，不要用 `MustDisk`。
- README 中保留 Go Reference badge：

```md
[![Go Reference](https://pkg.go.dev/badge/github.com/duolabmeng6/go-filesystem.svg)](https://pkg.go.dev/github.com/duolabmeng6/go-filesystem)
```

## pkg.go.dev 最佳实践

发版前必须确认：

- 根目录有 `go.mod`。
- 根目录有 pkg.go.dev 可识别的可再分发许可证文件，目前使用 `LICENSE`，内容是 MIT。
- README 有 pkg.go.dev 链接和 Go Reference badge。
- 包级 GoDoc 存在，当前是 `doc.go`。
- `go test ./...` 通过。
- 工作区干净后再打 tag。

pkg.go.dev 相关链接：

- 最佳实践：<https://pkg.go.dev/about#best-practices>
- 许可证政策：<https://pkg.go.dev/license-policy>

## 发版流程

用户说“发版”时，按下面流程执行。

1. 检查状态：

```sh
git status --short
git tag --list --sort=-version:refname
```

2. 运行测试：

```sh
go test ./...
```

3. 确认 README、`LICENSE`、`doc.go` 存在，并确认 README 里有 pkg.go.dev badge。

4. 如果有未提交改动，先让改动形成清晰提交：

```sh
git add -A
git commit -m "<合适的提交信息>"
```

5. 选择版本号：

- API 还未承诺稳定时，优先使用 `v0.x.y`。
- API 已准备好兼容性承诺时，使用 `v1.0.0` 或后续 `v1.x.y`。
- 不要覆盖已有 tag。

6. 打 tag 并推送：

```sh
git tag v0.1.0
git push origin main
git push origin v0.1.0
```

把 `v0.1.0` 替换为实际版本号。

7. 请求 pkg.go.dev 刷新：

```sh
GONOSUMDB=github.com/duolabmeng6/go-filesystem GOPROXY=proxy.golang.org go list -m github.com/duolabmeng6/go-filesystem@v0.1.0
```

或者打开：

```text
https://pkg.go.dev/github.com/duolabmeng6/go-filesystem@v0.1.0
```

## 当前发布注意事项

- 当前远程仓库是 `https://github.com/duolabmeng6/go-filesystem.git`。
- 用户希望主要服务中国用户，不需要维护英文 docs。
- 不要把文档改回英文文件名。
- `docs/目录.md` 是中文文档入口。
