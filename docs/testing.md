# 测试

这个库提供两类测试工具：

- `Fake` 和 `PersistentFake`，用于业务测试。
- `filesystemtest` 合约测试，用于驱动作者验证行为一致性。

## 使用 Fake 做业务测试

`Fake` 会把 manager 中指定名称的 disk 替换成临时本地 disk，并在 `t.Cleanup` 中恢复原 disk。

```go
func TestUploadAvatar(t *testing.T) {
	manager := filesystem.New(filesystem.WithDefaultDisk("local"))
	manager.MustExtend("local", local.NewFactory())
	_ = manager.ConfigureDisk("local", filesystem.DiskConfig{
		Driver: "local",
		Root:   t.TempDir(),
	})

	fake := filesystem.Fake(t, manager, "local")

	err := manager.Put(context.Background(), "avatars/me.png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}

	fake.AssertExists("avatars/me.png")
	fake.AssertContent("avatars/me.png", []byte("png"))
	fake.AssertCount("avatars", 1)
	fake.AssertMissing("avatars/missing.png")
}
```

测试中需要导入 `local` 包。它的 `init` 会注册核心 `filesystem.Fake` 使用的本地 fake factory。

## PersistentFake

需要测试后保留文件或指定目录时使用 `PersistentFake`：

```go
root := filepath.Join(os.TempDir(), "go-filesystem-debug")
fake, err := filesystem.PersistentFake(manager, "local", root)
if err != nil {
	t.Fatal(err)
}

_ = fake
```

`PersistentFake` 不会自动删除目录。

## 独立 Fake Disk

当被测代码直接接收 `*filesystem.Disk` 时，可以使用 `NewFakeDisk`：

```go
disk := filesystem.NewFakeDisk(t)

if err := disk.Put(context.Background(), "a.txt", []byte("a")); err != nil {
	t.Fatal(err)
}
disk.AssertExists("a.txt")
```

## 断言方法

| 方法 | 检查内容 |
| --- | --- |
| `AssertExists(paths...)` | 每个路径都存在。 |
| `AssertMissing(paths...)` | 每个路径都不存在。 |
| `AssertContent(path, expected)` | 文件字节一致。 |
| `AssertCount(dir, expected)` | `dir` 下递归文件数量一致。 |
| `AssertDirectoryEmpty(dir)` | `dir` 下没有文件或目录。 |

## 驱动合约测试

驱动包应使用 `filesystemtest` 保持行为一致：

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
	filesystemtest.RunURLContract(t, newDisk)
	filesystemtest.RunTemporaryURLContract(t, newDisk)
}
```

只运行驱动支持的合约。prefix-only 对象存储通常需要对象、列表、visibility、路径安全、URL 和临时 URL 合约，目录语义可以用 provider-specific 测试补充。

## 真实云服务商集成测试

仓库包含 OSS、S3-compatible 和 R2 集成测试。它们由环境变量控制，缺少必要变量时会跳过。

默认单测和 mock-backed 测试：

```sh
go test ./...
```

OSS：

```sh
go test ./drivers/oss -run TestIntegrationContracts -count=1 -v
```

S3-compatible：

```sh
go test ./drivers/s3 -run TestIntegrationContracts -count=1 -v
```

Cloudflare R2：

```sh
go test ./drivers/s3 -run TestR2IntegrationContracts -count=1 -v
```

`.env.example` 列出了需要的变量。真实凭据放在本地 `.env`、shell 环境、CI secret store 或云平台身份中。

## 错误断言

错误会包装 sentinel error。使用 `errors.Is`：

```go
err := disk.Delete(ctx, "missing.txt")
if errors.Is(err, filesystem.ErrNotFound) {
	// expected
}
```

常用 sentinel：

| Error | 含义 |
| --- | --- |
| `ErrNotFound` | 文件或对象不存在。 |
| `ErrAlreadyExists` | 禁止覆盖时目标已存在。 |
| `ErrInvalidPath` | 路径未通过规范化或安全检查。 |
| `ErrUnsupported` | 驱动不支持该能力。 |
| `ErrReadOnly` | disk 是只读的。 |
| `ErrInvalidVisibility` | visibility 不是 public/private/空默认值。 |
| `ErrInvalidExpiration` | 临时 URL 过期时间不在未来。 |
| `ErrPartialFailure` | 多步骤操作部分成功后失败。 |
