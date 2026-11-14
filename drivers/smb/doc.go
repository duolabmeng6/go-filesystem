// Package smb 提供 SMB2/3 网络共享驱动。通过 New 创建连接，或通过
// NewFactory 注册到 filesystem.Manager；使用结束后关闭适配器以释放网络连接。
package smb
