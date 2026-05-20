package filesystem_test

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/local"
)

func ExampleManager() {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "go-filesystem-example-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(root)

	manager, err := filesystem.NewFromConfig(ctx, filesystem.Config{
		Default: "local",
		Disks: map[string]filesystem.DiskConfig{
			"local": {
				Driver: "local",
				Root:   filepath.Join(root, "private"),
			},
			"public": {
				Driver:     "local",
				Root:       filepath.Join(root, "public"),
				BaseURL:    "/storage",
				Visibility: filesystem.VisibilityPublic,
			},
		},
	}, filesystem.WithDriver("local", local.NewFactory()))
	if err != nil {
		log.Fatal(err)
	}

	if err := manager.Put(ctx, "reports/hello.txt", []byte("hello")); err != nil {
		log.Fatal(err)
	}

	publicDisk, err := manager.Disk("public")
	if err != nil {
		log.Fatal(err)
	}
	if err := publicDisk.Put(ctx, "avatars/me.png", []byte("png")); err != nil {
		log.Fatal(err)
	}

	url, err := publicDisk.URL(ctx, "avatars/me.png")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(url)

	// Output:
	// /storage/avatars/me.png
}
