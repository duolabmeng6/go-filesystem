package s3

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
	runEnvIntegrationContracts(t, "S3")
}

func TestR2IntegrationContracts(t *testing.T) {
	runEnvIntegrationContracts(t, "R2")
}

func runEnvIntegrationContracts(t *testing.T, envPrefix string) {
	t.Helper()
	loadDotEnv(t)
	bucket := getenv(envPrefix, "TEST_BUCKET")
	region := getenv(envPrefix, "TEST_REGION")
	endpoint := getenv(envPrefix, "TEST_ENDPOINT")
	baseURL := getenv(envPrefix, "TEST_BASE_URL")
	accessKeyID := getenv(envPrefix, "ACCESS_KEY_ID")
	accessKeySecret := getenv(envPrefix, "ACCESS_KEY_SECRET")
	sessionToken := getenv(envPrefix, "SESSION_TOKEN")
	disableACL := boolEnv(envName(envPrefix, "TEST_DISABLE_ACL"))
	if bucket == "" || region == "" || accessKeyID == "" || accessKeySecret == "" {
		t.Skipf("set %s_TEST_BUCKET, %s_TEST_REGION, %s_ACCESS_KEY_ID, and %s_ACCESS_KEY_SECRET to run %s integration tests", envPrefix, envPrefix, envPrefix, envPrefix, envPrefix)
	}
	prefix := getenv(envPrefix, "TEST_PREFIX")
	if prefix == "" {
		prefix = "go-filesystem-tests/" + time.Now().UTC().Format("20060102150405")
	}
	prefix = strings.Trim(prefix, "/")
	newDisk := func(t testing.TB) *filesystem.Disk {
		t.Helper()
		disk, err := NewDisk(context.Background(), Config{
			Bucket:          bucket,
			Region:          region,
			Endpoint:        endpoint,
			AccessKeyID:     accessKeyID,
			AccessKeySecret: accessKeySecret,
			SessionToken:    sessionToken,
			BaseURL:         baseURL,
			UsePathStyle:    boolEnv(envName(envPrefix, "TEST_USE_PATH_STYLE")),
			DisableACL:      disableACL,
			PathPrefix:      prefix + "/" + safeTestPrefix(t.Name()),
		})
		if err != nil {
			t.Fatalf("new s3 disk: %v", err)
		}
		return disk
	}
	filesystemtest.RunObjectContract(t, newDisk)
	filesystemtest.RunListContract(t, newDisk)
	if !boolEnv(envName(envPrefix, "TEST_SKIP_VISIBILITY")) && !disableACL {
		filesystemtest.RunVisibilityContract(t, newDisk)
	}
	filesystemtest.RunPathSafetyContract(t, newDisk)
	runIntegrationURLContract(t, Config{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SessionToken:    sessionToken,
		BaseURL:         firstNonEmpty(baseURL, defaultBaseURL(bucket, region, endpoint)),
		UsePathStyle:    boolEnv(envName(envPrefix, "TEST_USE_PATH_STYLE")),
		DisableACL:      disableACL,
		PathPrefix:      prefix + "/url-contract",
	})
	runIntegrationTemporaryURLContract(t, Config{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SessionToken:    sessionToken,
		UsePathStyle:    boolEnv(envName(envPrefix, "TEST_USE_PATH_STYLE")),
		DisableACL:      disableACL,
		PathPrefix:      prefix + "/temporary-url-contract",
	})
}

func runIntegrationURLContract(t *testing.T, config Config) {
	t.Helper()
	if config.BaseURL == "" {
		t.Skip("set S3_TEST_BASE_URL or S3_TEST_ENDPOINT to run S3 URL integration test")
	}
	t.Run("s3 url", func(t *testing.T) {
		disk, err := NewDisk(context.Background(), config)
		if err != nil {
			t.Fatalf("new s3 disk: %v", err)
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
	t.Run("s3 temporary url", func(t *testing.T) {
		disk, err := NewDisk(context.Background(), config)
		if err != nil {
			t.Fatalf("new s3 disk: %v", err)
		}
		got, err := disk.TemporaryURL(context.Background(), "private file.txt", time.Now().Add(15*time.Minute))
		if err != nil {
			t.Fatalf("temporary url: %v", err)
		}
		if !strings.Contains(got, "private%20file.txt") && !strings.Contains(got, "private+file.txt") {
			t.Fatalf("temporary url does not contain escaped object key: %q", got)
		}
		if !strings.Contains(got, "X-Amz-Signature") {
			t.Fatalf("temporary url does not look signed: %q", got)
		}
	})
}

func defaultBaseURL(bucket string, region string, endpoint string) string {
	if bucket == "" {
		return ""
	}
	if endpoint != "" {
		endpoint = strings.TrimRight(endpoint, "/")
		endpoint = strings.TrimPrefix(endpoint, "https://")
		endpoint = strings.TrimPrefix(endpoint, "http://")
		return "https://" + bucket + "." + endpoint
	}
	if region == "" {
		return ""
	}
	return "https://" + bucket + ".s3." + region + ".amazonaws.com"
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
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || os.Getenv(key) != "" {
			continue
		}
		t.Setenv(key, value)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read .env: %v", err)
	}
}

func boolEnv(key string) bool {
	value := strings.ToLower(os.Getenv(key))
	return value == "1" || value == "true" || value == "yes"
}

func getenv(prefix string, key string) string {
	return os.Getenv(envName(prefix, key))
}

func envName(prefix string, key string) string {
	return prefix + "_" + key
}
