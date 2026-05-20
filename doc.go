// Package filesystem 用统一写法操作本地目录、S3 兼容存储和阿里云 OSS。
//
// 最小用法：注册 local 驱动，配置一个本地目录，然后直接写入和读取默认 disk。
//
//	manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
//		Default: "local",
//		Disks: map[string]filesystem.DiskConfig{
//			"local": {Driver: "local", Root: "storage/app"},
//		},
//	}, filesystem.WithDriver("local", local.NewFactory()))
//	if err != nil {
//		return err
//	}
//
//	if err := manager.Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//	data, err := manager.Get(ctx, "reports/a.txt")
//	if err != nil {
//		return err
//	}
//
// 也可以一次性配置多个 disk，例如 "local"、"s3"、"oss"。写文件时直接指定
// disk 名称即可切换存储：
//
//	if err := manager.MustDisk("local").Put(ctx, "reports/a.txt", data); err != nil {
//		return err
//	}
//	if err := manager.MustDisk("s3").Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//	if err := manager.MustDisk("oss").Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//
// 如果 disk 名称来自命令行、环境变量、租户配置或后台表单，请使用 Disk 并处理
// 返回的 error。
//
// 路径请使用类似 "avatars/me.png" 的相对路径，不要传操作系统绝对路径。
//
// 本地磁盘使用 local 包；S3 兼容存储使用 drivers/s3；阿里云 OSS 使用
// drivers/oss。完整中文教程见仓库 docs 目录。
package filesystem
