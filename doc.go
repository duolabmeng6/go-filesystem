// Package filesystem 提供一个小而稳定的 Go-first 文件与对象存储抽象。
//
// Manager 管理命名 disk 和默认 disk。Disk 提供业务代码使用的读写、列表、
// 复制、移动、元数据、visibility、URL 和临时 URL API。驱动实现 Adapter，
// 并可以按需实现原生 copy、公开 URL、临时 URL、目录操作和 visibility 控制等
// 可选能力。
//
// 路径是 slash 分隔的存储路径，不是操作系统路径。库会拒绝绝对路径、dot
// segment、重复斜杠、反斜杠、控制字符、Windows drive 路径、冒号 segment
// 和 Windows 保留名。列表操作可以使用空 prefix 表示根目录。
//
// 本地驱动位于 local 包。云存储驱动位于核心包之外，目前包括 drivers/s3 和
// drivers/oss。
package filesystem
