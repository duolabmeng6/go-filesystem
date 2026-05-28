package oss

import (
	"context"
	"fmt"
	"net/http"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/duolabmeng6/go-filesystem"
)

type Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string
	PathPrefix      string
	BaseURL         string
	Visibility      filesystem.Visibility
	UsePathStyle    bool
	UseCName        bool
	DisableSSL      bool
	HTTPClient      oss.HTTPClient
	Client          Client
}

type Client interface {
	PutObject(ctx context.Context, request *oss.PutObjectRequest, optFns ...func(*oss.Options)) (*oss.PutObjectResult, error)
	InitiateMultipartUpload(ctx context.Context, request *oss.InitiateMultipartUploadRequest, optFns ...func(*oss.Options)) (*oss.InitiateMultipartUploadResult, error)
	UploadPart(ctx context.Context, request *oss.UploadPartRequest, optFns ...func(*oss.Options)) (*oss.UploadPartResult, error)
	ListParts(ctx context.Context, request *oss.ListPartsRequest, optFns ...func(*oss.Options)) (*oss.ListPartsResult, error)
	CompleteMultipartUpload(ctx context.Context, request *oss.CompleteMultipartUploadRequest, optFns ...func(*oss.Options)) (*oss.CompleteMultipartUploadResult, error)
	AbortMultipartUpload(ctx context.Context, request *oss.AbortMultipartUploadRequest, optFns ...func(*oss.Options)) (*oss.AbortMultipartUploadResult, error)
	GetObject(ctx context.Context, request *oss.GetObjectRequest, optFns ...func(*oss.Options)) (*oss.GetObjectResult, error)
	HeadObject(ctx context.Context, request *oss.HeadObjectRequest, optFns ...func(*oss.Options)) (*oss.HeadObjectResult, error)
	DeleteObject(ctx context.Context, request *oss.DeleteObjectRequest, optFns ...func(*oss.Options)) (*oss.DeleteObjectResult, error)
	DeleteMultipleObjects(ctx context.Context, request *oss.DeleteMultipleObjectsRequest, optFns ...func(*oss.Options)) (*oss.DeleteMultipleObjectsResult, error)
	CopyObject(ctx context.Context, request *oss.CopyObjectRequest, optFns ...func(*oss.Options)) (*oss.CopyObjectResult, error)
	ListObjectsV2(ctx context.Context, request *oss.ListObjectsV2Request, optFns ...func(*oss.Options)) (*oss.ListObjectsV2Result, error)
	PutObjectAcl(ctx context.Context, request *oss.PutObjectAclRequest, optFns ...func(*oss.Options)) (*oss.PutObjectAclResult, error)
	GetObjectAcl(ctx context.Context, request *oss.GetObjectAclRequest, optFns ...func(*oss.Options)) (*oss.GetObjectAclResult, error)
	Presign(ctx context.Context, request any, optFns ...func(*oss.PresignOptions)) (*oss.PresignResult, error)
}

func New(config Config) (*Adapter, error) {
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	client := config.Client
	if client == nil {
		sdkConfig := oss.LoadDefaultConfig().
			WithRegion(config.Region).
			WithUsePathStyle(config.UsePathStyle).
			WithUseCName(config.UseCName).
			WithDisableSSL(config.DisableSSL)
		if config.Endpoint != "" {
			sdkConfig = sdkConfig.WithEndpoint(config.Endpoint)
		}
		if config.HTTPClient != nil {
			sdkConfig.HttpClient = config.HTTPClient
		} else {
			sdkConfig.HttpClient = http.DefaultClient
		}
		if config.AccessKeyID != "" || config.AccessKeySecret != "" || config.SecurityToken != "" {
			sdkConfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				config.AccessKeyID,
				config.AccessKeySecret,
				config.SecurityToken,
			))
		} else {
			sdkConfig.WithCredentialsProvider(credentials.NewAnonymousCredentialsProvider())
		}
		client = oss.NewClient(sdkConfig)
	}
	adapter := &Adapter{
		client:       client,
		bucket:       config.Bucket,
		baseURL:      trimBaseURL(config.BaseURL),
		urlEnabled:   config.BaseURL != "",
		visibility:   config.Visibility,
		capabilities: filesystem.NewCapabilitySet(filesystem.CapabilityCopy, filesystem.CapabilityTemporaryURL, filesystem.CapabilityVisibility, filesystem.CapabilityURL, filesystem.CapabilityMultipart),
	}
	if config.BaseURL == "" {
		delete(adapter.capabilities, filesystem.CapabilityURL)
	}
	return adapter, nil
}

func NewDisk(config Config) (*filesystem.Disk, error) {
	adapter, err := New(config)
	if err != nil {
		return nil, err
	}
	var diskAdapter filesystem.Adapter = adapter
	if config.PathPrefix != "" {
		diskAdapter, err = filesystem.Scoped(diskAdapter, config.PathPrefix)
		if err != nil {
			return nil, err
		}
	}
	return filesystem.NewDisk(diskAdapter), nil
}

func NewFactory() filesystem.DriverFactory {
	return func(ctx context.Context, config filesystem.DiskConfig) (filesystem.Adapter, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ossConfig := Config{
			Bucket:     stringOption(config.Options, "bucket"),
			Region:     stringOption(config.Options, "region"),
			Endpoint:   stringOption(config.Options, "endpoint"),
			BaseURL:    config.BaseURL,
			Visibility: config.Visibility,
		}
		if ossConfig.Bucket == "" {
			ossConfig.Bucket = config.Root
		}
		ossConfig.AccessKeyID = firstNonEmpty(stringOption(config.Options, "access_key_id"), stringOption(config.Options, "accessKeyID"))
		ossConfig.AccessKeySecret = firstNonEmpty(stringOption(config.Options, "access_key_secret"), stringOption(config.Options, "accessKeySecret"))
		ossConfig.SecurityToken = firstNonEmpty(stringOption(config.Options, "security_token"), stringOption(config.Options, "securityToken"))
		ossConfig.UsePathStyle = boolOption(config.Options, "use_path_style") || boolOption(config.Options, "usePathStyle")
		ossConfig.UseCName = boolOption(config.Options, "use_cname") || boolOption(config.Options, "useCName")
		ossConfig.DisableSSL = boolOption(config.Options, "disable_ssl") || boolOption(config.Options, "disableSSL")
		return New(ossConfig)
	}
}

func (c Config) withDefaults() (Config, error) {
	if c.Bucket == "" {
		return c, fmt.Errorf("%w: oss bucket is required", filesystem.ErrInvalidPath)
	}
	if c.Region == "" && c.Endpoint == "" && c.Client == nil {
		return c, fmt.Errorf("%w: oss region or endpoint is required", filesystem.ErrInvalidPath)
	}
	if c.Visibility == "" {
		c.Visibility = filesystem.VisibilityDefault
	}
	if !c.Visibility.Valid() {
		return c, filesystem.ErrInvalidVisibility
	}
	return c, nil
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
	if options == nil {
		return false
	}
	value, ok := options[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
