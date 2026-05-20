// Package filesystem 用一个 disk 别名切换本地目录、S3 兼容存储和阿里云 OSS。
//
// 推荐让业务代码只使用一个固定别名，例如 "storage"。开发环境可以让它指向
// 本地目录，生产环境可以让它指向 S3、R2、B2、MinIO 或 OSS。业务代码里的
// 上传、下载、生成链接逻辑不用跟着存储方式变化。
//
//	manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
//		Default: "storage",
//		Disks: map[string]filesystem.DiskConfig{
//			"storage": {Driver: "local", Root: "storage/app"},
//		},
//	}, filesystem.WithDriver("local", local.NewFactory()))
//	if err != nil {
//		return err
//	}
//
//	// 从这里开始就是稳定的业务代码。以后把 storage 切到 S3 或 OSS，
//	// 这段上传逻辑也不需要改。
//	if err := manager.Put(ctx, "reports/a.txt", []byte("hello")); err != nil {
//		return err
//	}
//
// 路径请使用类似 "avatars/me.png" 的相对路径，不要传操作系统绝对路径。
//
// 本地磁盘使用 local 包；S3 兼容存储使用 drivers/s3；阿里云 OSS 使用
// drivers/oss。完整中文教程见仓库 docs 目录。
package filesystem
