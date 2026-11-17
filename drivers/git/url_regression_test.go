package gitdriver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
)

func TestParseURLRepositoryMetadata(t *testing.T) {
	tests := []struct {
		name, raw, protocol, platform, user, owner, repo, path string
	}{
		{"http-user-and-port", "http://git-user:fixture-secret@localhost:8080/owner/project.git?auth=fixture#part", "https", "localhost", "git-user", "owner", "project", "owner/project"},
		{"https-user-and-port", "https://git-user:fixture-secret@example.com:8443/org/project.git?auth=fixture", "https", "example.com", "git-user", "org", "project", "org/project"},
		{"ipv6-http", "https://alice:fixture-secret@[::1]:8443/org/repo.git?q=a", "https", "::1", "alice", "org", "repo", "org/repo"},
		{"ipv6-ssh", "ssh://git@[2001:db8::1]:2222/org/repo.git", "ssh", "2001:db8::1", "git", "org", "repo", "org/repo"},
		{"scp", "git@example.com:org/repo.git", "ssh", "example.com", "git", "org", "repo", "org/repo"},
		{"scp-path-with-url", "git@example.com:org/http://repo.git", "ssh", "example.com", "git", "org/http:", "repo", "org/http:/repo"},
		{"file", "file:///tmp/project.git", "file", "local", "", "", "project", "/tmp/project.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseURL(tt.raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Protocol != tt.protocol || got.Platform != tt.platform || got.User != tt.user || got.Owner != tt.owner || got.Repo != tt.repo || got.RepositoryPath != tt.path {
				t.Fatalf("incorrect URL metadata: protocol=%q platform=%q user=%q owner=%q repo=%q path=%q", got.Protocol, got.Platform, got.User, got.Owner, got.Repo, got.RepositoryPath)
			}
		})
	}
}

func TestRejectIPv6SCPLikeURL(t *testing.T) {
	for _, raw := range []string{"git@[2001:db8::1]:org/repo.git", "[2001:db8::1]:org/repo.git"} {
		if _, err := ParseURL(raw); !errors.Is(err, filesystem.ErrInvalidPath) || !strings.Contains(err.Error(), "ssh://") {
			t.Fatal("SCP-style IPv6 must suggest the SSH URL format")
		}
		_, err := New(context.Background(), Config{URL: raw, Root: t.TempDir()})
		if !errors.Is(err, filesystem.ErrInvalidPath) || !strings.Contains(err.Error(), "ssh://") {
			t.Fatal("the adapter must reject unsupported SCP-style IPv6 before connecting")
		}
		_, err = NewFactory()(context.Background(), filesystem.DiskConfig{Root: t.TempDir(), Options: map[string]any{"url": raw}})
		if !errors.Is(err, filesystem.ErrInvalidPath) || !strings.Contains(err.Error(), "ssh://") {
			t.Fatal("the factory must reject unsupported SCP-style IPv6 before connecting")
		}
	}
}

func TestParseURLRejectsMalformedAddresses(t *testing.T) {
	for _, raw := range []string{"https://[::1", "https://example.com", "http://:8080/repo.git", "ftp://example.com/repo.git", "git@example.com:", "git@[not-ip]:owner/repo.git", ""} {
		if _, err := ParseURL(raw); !errors.Is(err, filesystem.ErrInvalidPath) {
			t.Errorf("invalid URL %q did not return ErrInvalidPath", raw)
		}
	}
}
