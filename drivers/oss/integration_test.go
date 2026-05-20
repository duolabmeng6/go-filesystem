package oss

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duolabmeng6/go-filesystem"
	"github.com/duolabmeng6/go-filesystem/filesystemtest"
)

func TestIntegrationContracts(t *testing.T) {
	loadDotEnv(t)
	bucket := os.Getenv("OSS_TEST_BUCKET")
	region := os.Getenv("OSS_TEST_REGION")
	endpoint := os.Getenv("OSS_TEST_ENDPOINT")
	baseURL := os.Getenv("OSS_TEST_BASE_URL")
	accessKeyID := firstNonEmpty(os.Getenv("OSS_ACCESS_KEY_ID"), os.Getenv("ALIYUN_ACCESS_KEY_ID"), os.Getenv("aliyun_access_key"))
	accessKeySecret := firstNonEmpty(os.Getenv("OSS_ACCESS_KEY_SECRET"), os.Getenv("ALIYUN_ACCESS_KEY_SECRET"), os.Getenv("aliyun_access_secret"))
	securityToken := firstNonEmpty(os.Getenv("OSS_SECURITY_TOKEN"), os.Getenv("ALIYUN_SECURITY_TOKEN"))
	if bucket == "" || (region == "" && endpoint == "") || accessKeyID == "" || accessKeySecret == "" {
		t.Skip("set OSS_TEST_BUCKET, OSS_TEST_REGION or OSS_TEST_ENDPOINT, OSS_ACCESS_KEY_ID, and OSS_ACCESS_KEY_SECRET to run OSS integration tests")
	}
	prefix := os.Getenv("OSS_TEST_PREFIX")
	if prefix == "" {
		prefix = "go-filesystem-tests/" + time.Now().UTC().Format("20060102150405")
	}
	prefix = strings.Trim(prefix, "/")
	newDisk := func(t testing.TB) *filesystem.Disk {
		t.Helper()
		disk, err := NewDisk(Config{
			Bucket:          bucket,
			Region:          region,
			Endpoint:        endpoint,
			AccessKeyID:     accessKeyID,
			AccessKeySecret: accessKeySecret,
			SecurityToken:   securityToken,
			BaseURL:         baseURL,
			PathPrefix:      prefix + "/" + safeTestPrefix(t.Name()),
		})
		if err != nil {
			t.Fatalf("new oss disk: %v", err)
		}
		return disk
	}
	filesystemtest.RunObjectContract(t, newDisk)
	filesystemtest.RunListContract(t, newDisk)
	filesystemtest.RunVisibilityContract(t, newDisk)
	filesystemtest.RunPathSafetyContract(t, newDisk)
	runIntegrationURLContract(t, Config{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SecurityToken:   securityToken,
		BaseURL:         firstNonEmpty(baseURL, defaultBaseURL(bucket, endpoint)),
		PathPrefix:      prefix + "/url-contract",
	})
	runIntegrationTemporaryURLContract(t, Config{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SecurityToken:   securityToken,
		PathPrefix:      prefix + "/temporary-url-contract",
	})
}

func runIntegrationURLContract(t *testing.T, config Config) {
	t.Helper()
	if config.BaseURL == "" {
		t.Skip("set OSS_TEST_BASE_URL or OSS_TEST_ENDPOINT to run OSS URL integration test")
	}
	t.Run("oss url", func(t *testing.T) {
		disk, err := NewDisk(config)
		if err != nil {
			t.Fatalf("new oss disk: %v", err)
		}
		got, err := disk.URL(context.Background(), "dir/hello world#?.txt")
		if err != nil {
			t.Fatalf("url: %v", err)
		}
		want := strings.TrimRight(config.BaseURL, "/") + "/" + config.PathPrefix + "/dir/hello%20world%23%3F.txt"
		if got != want {
			t.Fatalf("url=%q, want %q", got, want)
		}
	})
}

func runIntegrationTemporaryURLContract(t *testing.T, config Config) {
	t.Helper()
	t.Run("oss temporary url", func(t *testing.T) {
		disk, err := NewDisk(config)
		if err != nil {
			t.Fatalf("new oss disk: %v", err)
		}
		got, err := disk.TemporaryURL(context.Background(), "private file.txt", time.Now().Add(15*time.Minute))
		if err != nil {
			t.Fatalf("temporary url: %v", err)
		}
		if !strings.Contains(got, "private%20file.txt") && !strings.Contains(got, "private+file.txt") {
			t.Fatalf("temporary url does not contain escaped object key: %q", got)
		}
		if !strings.Contains(got, "Signature") && !strings.Contains(got, "signature") {
			t.Fatalf("temporary url does not look signed: %q", got)
		}
	})
}

func defaultBaseURL(bucket string, endpoint string) string {
	if bucket == "" || endpoint == "" {
		return ""
	}
	endpoint = strings.TrimRight(endpoint, "/")
	endpoint = strings.TrimPrefix(endpoint, "https://")
	endpoint = strings.TrimPrefix(endpoint, "http://")
	return "https://" + bucket + "." + endpoint
}

func safeTestPrefix(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	prefix := strings.Trim(b.String(), "-")
	if prefix == "" {
		return "test"
	}
	return prefix
}

func loadDotEnv(t testing.TB) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	for {
		envPath := filepath.Join(cwd, ".env")
		if _, err := os.Stat(envPath); err == nil {
			loadDotEnvFile(t, envPath)
			return
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return
		}
		cwd = parent
	}
}

func loadDotEnvFile(t testing.TB, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open .env: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		t.Setenv(key, value)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read .env: %v", err)
	}
}
