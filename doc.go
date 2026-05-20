// Package filesystem 用统一写法操作本地文件、S3 兼容存储和阿里云 OSS。
//
// 最常见的用法是先创建一个 manager，然后通过默认 disk 或命名 disk 写入、
// 读取、删除文件：
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
//	if err := manager.Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//
// 路径请使用类似 "avatars/me.png" 的相对路径，不要传操作系统绝对路径。
//
// 本地磁盘使用 local 包；S3 兼容存储使用 drivers/s3；阿里云 OSS 使用
// drivers/oss。完整教程见仓库 docs 目录。
package filesystem
