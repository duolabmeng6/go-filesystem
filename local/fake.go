package local

import "github.com/duolabmeng6/go-filesystem"

func init() {
	filesystem.RegisterFakeLocalFactory(func(config filesystem.FakeLocalConfig) (*filesystem.Disk, error) {
		return NewDisk(Config{
			Root:       config.Root,
			Visibility: config.Visibility,
		})
	})
}
