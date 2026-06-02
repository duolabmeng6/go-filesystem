# 配置 Git 同步储存

Git driver 会把远端仓库 clone 到本地缓存目录，然后让这个缓存目录像普通文件储存一样读写。Git 只负责同步协议，业务代码仍然调用 `Put`、`Get`、`List`、`Move`、`Delete` 这些文件方法。

## 注册 driver

```go
manager, err := filesystem.NewFromConfig(
	ctx,
	filesystem.Config{
		Default: "git-docs",
		Disks: map[string]filesystem.DiskConfig{
			"git-docs": {
				Driver: "git",
				Root:   "data/git-cache/docs",
				Options: map[string]any{
					"url":       "https://github.com/owner/repo.git",
					"branch":    "main",
					"auth_mode": "none",
					"auto_pull": true,
				},
			},
		},
	},
	filesystem.WithDriver("git", gitdriver.NewFactory()),
)
if err != nil {
	return err
}
```

`Root` 是本地缓存工作区。首次打开时如果目录为空，driver 会 clone 远端仓库；后续打开已有缓存时会复用这个工作区。

## 只读仓库

公开仓库可以不配置凭据：

```go
disk, err := manager.Disk("git-docs")
if err != nil {
	return err
}

items, err := disk.Files(ctx, "")
```

如果业务已经确认这个仓库只能读取，可以在 `DiskConfig` 上设置 `ReadOnly: true`。只读 disk 会继续允许浏览、读取和列表，写入、删除、移动、复制会返回 `filesystem.ErrReadOnly`。

## HTTPS 凭据

GitHub、GitLab 等平台通常把访问令牌放在 `password` 字段：

```go
"git-docs": {
	Driver: "git",
	Root:   "data/git-cache/docs",
	Options: map[string]any{
		"url":       "https://github.com/owner/repo.git",
		"branch":    "main",
		"auth_mode": "password",
		"username":  os.Getenv("GIT_USERNAME"),
		"password":  os.Getenv("GIT_TOKEN"),
	},
}
```

## SSH 私钥

SSH 私钥可以直接传入内容，不需要依赖用户机器上的默认 key 文件：

```go
"git-docs": {
	Driver: "git",
	Root:   "data/git-cache/docs",
	Options: map[string]any{
		"url":                    "git@github.com:owner/repo.git",
		"branch":                 "main",
		"auth_mode":              "private_key",
		"username":               "git",
		"private_key":            os.Getenv("GIT_PRIVATE_KEY"),
		"private_key_passphrase": os.Getenv("GIT_PRIVATE_KEY_PASSPHRASE"),
	},
}
```

## 手动同步

文件操作会先写入本地缓存。需要把本地变更提交并推送时，可以取出底层 adapter 调用同步方法：

```go
disk := manager.MustDisk("git-docs")

if err := disk.Put(ctx, "notes/a.md", []byte("hello")); err != nil {
	return err
}

adapter, ok := disk.Adapter().(*gitdriver.Adapter)
if ok {
	err := adapter.Sync(ctx, "同步文件变更")
	if err != nil {
		return err
	}
}
```

`Sync` 会先 pull 远端更新，再 commit 本地变更并 push。没有本地变更时不会创建空提交。

## 地址识别和连接测试

```go
info, err := gitdriver.ParseURL("git@github.com:owner/repo.git")
```

支持：

- `https://github.com/owner/repo.git`
- `git@github.com:owner/repo.git`
- `ssh://git@github.com/owner/repo.git`

连接测试：

```go
result, err := gitdriver.TestConnection(ctx, gitdriver.Config{
	URL:      "https://github.com/owner/repo.git",
	AuthMode: gitdriver.AuthModeNone,
})
```

`result.Mode` 会返回 `read_only`、`read_write` 或 `unavailable`。没有写入凭据但能读取的仓库会按只读模式处理；有写入凭据时，driver 会创建并删除一个 `ll-filebrowser-probe-*` 临时分支来确认实际写权限。
