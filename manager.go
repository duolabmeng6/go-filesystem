package filesystem

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type Manager struct {
	mu          sync.RWMutex
	defaultDisk string
	configs     map[string]DiskConfig
	disks       map[string]*Disk
	factories   map[string]DriverFactory
}

func New(opts ...ManagerOption) *Manager {
	m := &Manager{
		configs:   make(map[string]DiskConfig),
		disks:     make(map[string]*Disk),
		factories: make(map[string]DriverFactory),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m
}

func NewFromConfig(ctx context.Context, config Config, opts ...ManagerOption) (*Manager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := New()
	m.mu.Lock()
	m.defaultDisk = config.Default
	for name, diskConfig := range config.Disks {
		m.configs[name] = cloneDiskConfig(diskConfig)
	}
	m.mu.Unlock()
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}
	return m, nil
}

func (m *Manager) ConfigureDisk(name string, config DiskConfig) error {
	if name == "" {
		return ErrDiskNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configs[name] = cloneDiskConfig(config)
	delete(m.disks, name)
	return nil
}

func WithDriver(driver string, factory DriverFactory) ManagerOption {
	return func(m *Manager) {
		if driver != "" && factory != nil {
			m.factories[driver] = factory
		}
	}
}

func (m *Manager) Disk(name string) (*Disk, error) {
	m.mu.RLock()
	if disk, ok := m.disks[name]; ok {
		m.mu.RUnlock()
		return disk, nil
	}
	config, ok := m.configs[name]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s", ErrDiskNotFound, name)
	}
	factory, ok := m.factories[config.Driver]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("%w: %s", ErrDriverNotFound, config.Driver)
	}
	m.mu.RUnlock()

	disk, err := m.buildWithFactory(context.Background(), name, config, factory)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.disks[name]; ok {
		return existing, nil
	}
	m.disks[name] = disk
	return disk, nil
}

// MustDisk 返回指定名称的 disk，适合在 disk 名称由代码或配置固定控制时写链式调用。
//
//	m.MustDisk("local").Put(ctx, "reports/a.txt", data)
//	m.MustDisk("s3").Put(ctx, "reports/a.txt", data)
//	m.MustDisk("oss").Put(ctx, "reports/a.txt", data)
//
// 如果 disk 不存在、驱动没有注册或初始化失败，MustDisk 会 panic。disk 名称来自命令行、
// 环境变量、租户配置或后台表单时，请使用 Disk 并处理返回的 error。
func (m *Manager) MustDisk(name string) *Disk {
	disk, err := m.Disk(name)
	if err != nil {
		panic(err)
	}
	return disk
}

func (m *Manager) DefaultDisk() (*Disk, error) {
	m.mu.RLock()
	name := m.defaultDisk
	m.mu.RUnlock()
	if name == "" {
		return nil, ErrDiskNotFound
	}
	return m.Disk(name)
}

func (m *Manager) SetDefaultDisk(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.disks[name]; !ok {
		if _, ok := m.configs[name]; !ok {
			return fmt.Errorf("%w: %s", ErrDiskNotFound, name)
		}
	}
	m.defaultDisk = name
	return nil
}

func (m *Manager) RegisterDisk(name string, disk *Disk) error {
	if name == "" || disk == nil {
		return ErrDiskNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.disks[name]; ok {
		return fmt.Errorf("%w: %s", ErrDiskAlreadyRegistered, name)
	}
	if _, ok := m.configs[name]; ok {
		return fmt.Errorf("%w: %s", ErrDiskAlreadyRegistered, name)
	}
	m.disks[name] = disk.withName(name)
	return nil
}

func (m *Manager) ReplaceDisk(name string, disk *Disk) error {
	if name == "" || disk == nil {
		return ErrDiskNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disks[name] = disk.withName(name)
	return nil
}

func (m *Manager) Extend(driver string, factory DriverFactory) error {
	if driver == "" || factory == nil {
		return ErrDriverNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.factories[driver]; ok {
		return fmt.Errorf("%w: %s", ErrDriverAlreadyRegistered, driver)
	}
	m.factories[driver] = factory
	return nil
}

func (m *Manager) MustExtend(driver string, factory DriverFactory) {
	if err := m.Extend(driver, factory); err != nil {
		panic(err)
	}
}

func (m *Manager) Build(ctx context.Context, config DiskConfig) (*Disk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	factory, ok := m.factories[config.Driver]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrDriverNotFound, config.Driver)
	}
	return m.buildWithFactory(ctx, "", config, factory)
}

func (m *Manager) removeDisk(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.disks, name)
}

func (m *Manager) snapshotDisk(name string) (*Disk, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	disk, ok := m.disks[name]
	return disk, ok
}

func (m *Manager) buildWithFactory(ctx context.Context, name string, config DiskConfig, factory DriverFactory) (*Disk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	adapter, err := factory(ctx, cloneDiskConfig(config))
	if err != nil {
		return nil, err
	}
	if config.PathPrefix != "" {
		adapter, err = Scoped(adapter, config.PathPrefix)
		if err != nil {
			return nil, err
		}
	}
	if config.ReadOnly {
		adapter = ReadOnly(adapter)
	}
	return NewDisk(adapter, WithDiskName(name)), nil
}

func cloneDiskConfig(config DiskConfig) DiskConfig {
	clone := config
	if config.Options != nil {
		clone.Options = make(map[string]any, len(config.Options))
		for k, v := range config.Options {
			clone.Options[k] = v
		}
	}
	return clone
}

func (m *Manager) Put(ctx context.Context, path string, data []byte, opts ...WriteOption) error {
	disk, err := m.DefaultDisk()
	if err != nil {
		return err
	}
	return disk.Put(ctx, path, data, opts...)
}

func (m *Manager) Write(ctx context.Context, path string, r io.Reader, opts ...WriteOption) error {
	disk, err := m.DefaultDisk()
	if err != nil {
		return err
	}
	return disk.Write(ctx, path, r, opts...)
}

func (m *Manager) CreateMultipartUpload(ctx context.Context, path string, opts ...WriteOption) (MultipartUpload, error) {
	disk, err := m.DefaultDisk()
	if err != nil {
		return MultipartUpload{}, err
	}
	return disk.CreateMultipartUpload(ctx, path, opts...)
}

func (m *Manager) UploadPart(ctx context.Context, path string, uploadID string, partNumber int, r io.Reader, size int64) (MultipartUploadPart, error) {
	disk, err := m.DefaultDisk()
	if err != nil {
		return MultipartUploadPart{}, err
	}
	return disk.UploadPart(ctx, path, uploadID, partNumber, r, size)
}

func (m *Manager) ListMultipartUploadParts(ctx context.Context, path string, uploadID string) ([]MultipartUploadPart, error) {
	disk, err := m.DefaultDisk()
	if err != nil {
		return nil, err
	}
	return disk.ListMultipartUploadParts(ctx, path, uploadID)
}

func (m *Manager) CompleteMultipartUpload(ctx context.Context, path string, uploadID string, parts []MultipartUploadPart, opts ...WriteOption) error {
	disk, err := m.DefaultDisk()
	if err != nil {
		return err
	}
	return disk.CompleteMultipartUpload(ctx, path, uploadID, parts, opts...)
}

func (m *Manager) AbortMultipartUpload(ctx context.Context, path string, uploadID string) error {
	disk, err := m.DefaultDisk()
	if err != nil {
		return err
	}
	return disk.AbortMultipartUpload(ctx, path, uploadID)
}

func (m *Manager) Get(ctx context.Context, path string) ([]byte, error) {
	disk, err := m.DefaultDisk()
	if err != nil {
		return nil, err
	}
	return disk.Get(ctx, path)
}

func (m *Manager) Exists(ctx context.Context, path string) (bool, error) {
	disk, err := m.DefaultDisk()
	if err != nil {
		return false, err
	}
	return disk.Exists(ctx, path)
}
