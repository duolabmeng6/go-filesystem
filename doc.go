// Package filesystem 用统一写法操作本地目录、S3 兼容存储和阿里云 OSS。
//
// 推荐一次性配置多个 disk，例如 "local"、"s3"、"oss"。普通业务可以使用默认
// disk；需要动态切换时，可以根据命令参数、租户配置或后台表单传入的名称调用
// manager.Disk(name)，把文件写到指定驱动。
//
//	manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
//		Default: "local",
//		Disks: map[string]filesystem.DiskConfig{
//			"local": {Driver: "local", Root: "storage/app"},
//			"s3":    {Driver: "s3", Options: map[string]any{"bucket": "my-bucket"}},
//		},
//	}, filesystem.WithDriver("local", local.NewFactory()), filesystem.WithDriver("s3", s3driver.NewFactory()))
//	if err != nil {
//		return err
//	}
//
//	disk, err := manager.Disk("s3")
//	if err != nil {
//		return err
//	}
//	if err := disk.Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//
// 路径请使用类似 "avatars/me.png" 的相对路径，不要传操作系统绝对路径。
//
// 本地磁盘使用 local 包；S3 兼容存储使用 drivers/s3；阿里云 OSS 使用
// drivers/oss。完整中文教程见仓库 docs 目录。
package filesystem
