package filesystem

import "context"

type Config struct {
	Default string
	Disks   map[string]DiskConfig
}

type DiskConfig struct {
	Driver     string
	Root       string
	BaseURL    string
	Visibility Visibility
	PathPrefix string
	ReadOnly   bool
	Options    map[string]any
}

type DriverFactory func(ctx context.Context, config DiskConfig) (Adapter, error)

type ManagerOption func(*Manager)

func WithDefaultDisk(name string) ManagerOption {
	return func(m *Manager) {
		m.defaultDisk = name
	}
}
