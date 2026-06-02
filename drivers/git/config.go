package gitdriver

import (
	"context"
	"fmt"
	"strings"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
)

const (
	AuthModeNone       = "none"
	AuthModePassword   = "password"
	AuthModePrivateKey = "private_key"

	defaultRemoteName = "origin"
)

type Config struct {
	URL                  string
	Branch               string
	Root                 string
	Visibility           filesystem.Visibility
	ReadOnly             bool
	AuthMode             string
	Username             string
	Password             string
	PrivateKey           string
	PrivateKeyPassphrase string
	StrictHostKey        bool
	AutoPull             bool
	CommitName           string
	CommitEmail          string
}

func NewFactory() filesystem.DriverFactory {
	return func(ctx context.Context, config filesystem.DiskConfig) (filesystem.Adapter, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return New(ctx, Config{
			URL:                  firstNonEmpty(stringOption(config.Options, "url"), stringOption(config.Options, "remote_url")),
			Branch:               stringOption(config.Options, "branch"),
			Root:                 config.Root,
			Visibility:           config.Visibility,
			ReadOnly:             config.ReadOnly || boolOption(config.Options, "read_only"),
			AuthMode:             stringOption(config.Options, "auth_mode"),
			Username:             stringOption(config.Options, "username"),
			Password:             firstNonEmpty(stringOption(config.Options, "password"), stringOption(config.Options, "token")),
			PrivateKey:           stringOption(config.Options, "private_key"),
			PrivateKeyPassphrase: stringOption(config.Options, "private_key_passphrase"),
			StrictHostKey:        boolOption(config.Options, "strict_host_key"),
			AutoPull:             boolOptionDefault(config.Options, "auto_pull", true),
			CommitName:           stringOption(config.Options, "commit_name"),
			CommitEmail:          stringOption(config.Options, "commit_email"),
		})
	}
}

func NewDisk(ctx context.Context, config Config) (*filesystem.Disk, error) {
	adapter, err := New(ctx, config)
	if err != nil {
		return nil, err
	}
	var diskAdapter filesystem.Adapter = adapter
	if config.ReadOnly {
		diskAdapter = filesystem.ReadOnly(diskAdapter)
	}
	return filesystem.NewDisk(diskAdapter), nil
}

func (c Config) withDefaults() (Config, error) {
	c.URL = strings.TrimSpace(c.URL)
	c.Branch = strings.TrimSpace(c.Branch)
	c.Root = strings.TrimSpace(c.Root)
	c.AuthMode = normalizeAuthMode(c.AuthMode, c.PrivateKey, c.Password)
	c.Username = strings.TrimSpace(c.Username)
	c.Password = strings.TrimSpace(c.Password)
	c.PrivateKey = strings.TrimSpace(c.PrivateKey)
	c.CommitName = strings.TrimSpace(c.CommitName)
	c.CommitEmail = strings.TrimSpace(c.CommitEmail)
	if c.Visibility == "" {
		c.Visibility = filesystem.VisibilityPrivate
	}
	if c.CommitName == "" {
		c.CommitName = "ll-filebrowser"
	}
	if c.CommitEmail == "" {
		c.CommitEmail = "ll-filebrowser@example.local"
	}
	if c.URL == "" {
		return c, fmt.Errorf("%w: git url is required", filesystem.ErrInvalidPath)
	}
	if c.Root == "" {
		return c, fmt.Errorf("%w: git cache root is required", filesystem.ErrInvalidPath)
	}
	if !c.Visibility.Valid() {
		return c, filesystem.ErrInvalidVisibility
	}
	if c.AuthMode == "" {
		return c, fmt.Errorf("%w: git auth mode is invalid", filesystem.ErrInvalidPath)
	}
	if c.AuthMode == AuthModePassword && c.Password == "" {
		return c, fmt.Errorf("%w: git password or token is required", filesystem.ErrInvalidPath)
	}
	if c.AuthMode == AuthModePrivateKey && c.PrivateKey == "" {
		return c, fmt.Errorf("%w: git private key is required", filesystem.ErrInvalidPath)
	}
	return c, nil
}

func normalizeAuthMode(mode string, privateKey string, password string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto":
		if strings.TrimSpace(privateKey) != "" {
			return AuthModePrivateKey
		}
		if strings.TrimSpace(password) != "" {
			return AuthModePassword
		}
		return AuthModeNone
	case AuthModeNone, "public", "readonly", "read_only":
		return AuthModeNone
	case AuthModePassword, "https", "token", "basic":
		return AuthModePassword
	case AuthModePrivateKey, "private-key", "ssh", "key":
		return AuthModePrivateKey
	default:
		return ""
	}
}

func (c Config) authMethod() (transport.AuthMethod, error) {
	switch c.AuthMode {
	case AuthModeNone:
		return nil, nil
	case AuthModePassword:
		username := c.Username
		if username == "" {
			username = "git"
		}
		return &githttp.BasicAuth{Username: username, Password: c.Password}, nil
	case AuthModePrivateKey:
		user := c.Username
		if user == "" {
			user = gitUserFromURL(c.URL)
		}
		if user == "" {
			user = "git"
		}
		auth, err := gitssh.NewPublicKeys(user, []byte(c.PrivateKey), c.PrivateKeyPassphrase)
		if err != nil {
			return nil, fmt.Errorf("%w: git private key could not be parsed: %v", filesystem.ErrInvalidPath, err)
		}
		if !c.StrictHostKey {
			auth.HostKeyCallback = ssh.InsecureIgnoreHostKey()
		}
		return auth, nil
	default:
		return nil, fmt.Errorf("%w: git auth mode is invalid", filesystem.ErrInvalidPath)
	}
}

func (c Config) hasWriteCredential() bool {
	return c.AuthMode == AuthModePassword || c.AuthMode == AuthModePrivateKey
}

func stringOption(options map[string]any, key string) string {
	if options == nil {
		return ""
	}
	value, ok := options[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func boolOption(options map[string]any, key string) bool {
	return boolOptionDefault(options, key, false)
}

func boolOptionDefault(options map[string]any, key string, fallback bool) bool {
	if options == nil {
		return fallback
	}
	value, ok := options[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func gitUserFromURL(raw string) string {
	info, err := ParseURL(raw)
	if err != nil {
		return ""
	}
	if info.User != "" {
		return info.User
	}
	return "git"
}
