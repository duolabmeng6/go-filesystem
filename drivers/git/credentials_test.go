package gitdriver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/duolabmeng6/go-filesystem"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"golang.org/x/crypto/ssh"
)

func TestGitPasswordPreserved(t *testing.T) {
	for _, password := range []string{"  fixture-secret  ", "   "} {
		t.Run("defaults", func(t *testing.T) {
			config, err := (Config{URL: "https://example.invalid/repo.git", Root: t.TempDir(), Password: password}).withDefaults()
			if err != nil {
				t.Fatal(err)
			}
			if config.AuthMode != AuthModePassword || config.Password != password {
				t.Fatal("password or auth mode changed")
			}
			auth, err := config.authMethod()
			if err != nil {
				t.Fatal(err)
			}
			if auth.(*githttp.BasicAuth).Password != password {
				t.Fatal("auth password changed")
			}
		})
		t.Run("factory", func(t *testing.T) {
			adapter, err := NewFactory()(context.Background(), filesystem.DiskConfig{Root: t.TempDir(), Options: map[string]any{"url": seedRepository(t), "password": password, "token": "fallback"}})
			if err != nil {
				t.Fatal(err)
			}
			got := adapter.(*Adapter).config
			if got.Password != password || got.AuthMode != AuthModePassword {
				t.Fatal("factory changed password or auth mode")
			}
		})
		t.Run("connection", func(t *testing.T) {
			var got string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, got, _ = r.BasicAuth()
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer server.Close()
			result, err := TestConnection(context.Background(), Config{URL: server.URL + "/repo.git", Username: "git", Password: password})
			if err != nil {
				t.Fatal(err)
			}
			if got != password {
				t.Fatal("connection did not send the exact password")
			}
			if strings.Contains(result.Message, password) {
				t.Fatal("connection result exposed password")
			}
		})
	}
}

func TestGitPrivateKeyPassphrasePreserved(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, passphrase := range []string{"  fixture-passphrase  ", "   "} {
		block, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "test", []byte(passphrase))
		if err != nil {
			t.Fatal(err)
		}
		key := string(pem.EncodeToMemory(block))
		config, err := (Config{URL: "ssh://git@127.0.0.1:1/repo.git", Root: t.TempDir(), PrivateKey: key, PrivateKeyPassphrase: passphrase}).withDefaults()
		if err != nil {
			t.Fatal(err)
		}
		if config.PrivateKeyPassphrase != passphrase {
			t.Fatal("defaults changed key passphrase")
		}
		if _, err := config.authMethod(); err != nil {
			t.Fatalf("parse key: %v", err)
		}
		result, err := TestConnection(context.Background(), config)
		if err != nil {
			t.Fatalf("test connection: %v", err)
		}
		if strings.Contains(result.Message, passphrase) {
			t.Fatal("connection result exposed passphrase")
		}
	}
}
