package s3

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/duolabmeng6/go-filesystem"
)

type Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	AccessKeySecret string
	SessionToken    string
	PathPrefix      string
	BaseURL         string
	Visibility      filesystem.Visibility
	UsePathStyle    bool
	DisableSSL      bool
	DisableACL      bool
	Client          Client
	PresignClient   PresignClient
}

type Client interface {
	PutObject(ctx context.Context, input *awss3.PutObjectInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, input *awss3.CreateMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, input *awss3.UploadPartInput, optFns ...func(*awss3.Options)) (*awss3.UploadPartOutput, error)
	ListParts(ctx context.Context, input *awss3.ListPartsInput, optFns ...func(*awss3.Options)) (*awss3.ListPartsOutput, error)
	CompleteMultipartUpload(ctx context.Context, input *awss3.CompleteMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, input *awss3.AbortMultipartUploadInput, optFns ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error)
	GetObject(ctx context.Context, input *awss3.GetObjectInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	HeadObject(ctx context.Context, input *awss3.HeadObjectInput, optFns ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, input *awss3.DeleteObjectInput, optFns ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	CopyObject(ctx context.Context, input *awss3.CopyObjectInput, optFns ...func(*awss3.Options)) (*awss3.CopyObjectOutput, error)
	ListObjectsV2(ctx context.Context, input *awss3.ListObjectsV2Input, optFns ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
	PutObjectAcl(ctx context.Context, input *awss3.PutObjectAclInput, optFns ...func(*awss3.Options)) (*awss3.PutObjectAclOutput, error)
	GetObjectAcl(ctx context.Context, input *awss3.GetObjectAclInput, optFns ...func(*awss3.Options)) (*awss3.GetObjectAclOutput, error)
}

type PresignClient interface {
	PresignGetObject(ctx context.Context, input *awss3.GetObjectInput, optFns ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

func New(ctx context.Context, config Config) (*Adapter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := config.withDefaults()
	if err != nil {
		return nil, err
	}
	client := config.Client
	presigner := config.PresignClient
	if client == nil {
		loadOptions := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion(config.Region),
		}
		if config.AccessKeyID != "" || config.AccessKeySecret != "" || config.SessionToken != "" {
			loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
				config.AccessKeyID,
				config.AccessKeySecret,
				config.SessionToken,
			)))
		}
		awsConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
		if err != nil {
			return nil, err
		}
		client = awss3.NewFromConfig(awsConfig, func(o *awss3.Options) {
			o.UsePathStyle = config.UsePathStyle
			applyS3CompatibilityOptions(o)
			if config.Endpoint != "" {
				endpoint := withScheme(config.Endpoint, config.DisableSSL)
				o.BaseEndpoint = aws.String(endpoint)
			}
		})
	}
	if presigner == nil {
		if sdkClient, ok := client.(*awss3.Client); ok {
			presigner = awss3.NewPresignClient(sdkClient)
		} else {
			presigner = unsupportedPresignClient{}
		}
	}
	adapter := &Adapter{
		client:       client,
		presigner:    presigner,
		bucket:       config.Bucket,
		baseURL:      trimBaseURL(config.BaseURL),
		urlEnabled:   config.BaseURL != "",
		visibility:   config.Visibility,
		disableACL:   config.DisableACL,
		capabilities: filesystem.NewCapabilitySet(filesystem.CapabilityCopy, filesystem.CapabilityTemporaryURL, filesystem.CapabilityVisibility, filesystem.CapabilityURL, filesystem.CapabilityMultipart),
	}
	if config.BaseURL == "" {
		delete(adapter.capabilities, filesystem.CapabilityURL)
	}
	if config.DisableACL {
		delete(adapter.capabilities, filesystem.CapabilityVisibility)
	}
	return adapter, nil
}

func NewDisk(ctx context.Context, config Config) (*filesystem.Disk, error) {
	adapter, err := New(ctx, config)
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
		s3Config := Config{
			Bucket:       stringOption(config.Options, "bucket"),
			Region:       stringOption(config.Options, "region"),
			Endpoint:     stringOption(config.Options, "endpoint"),
			BaseURL:      config.BaseURL,
			Visibility:   config.Visibility,
			UsePathStyle: boolOption(config.Options, "use_path_style") || boolOption(config.Options, "usePathStyle"),
			DisableSSL:   boolOption(config.Options, "disable_ssl") || boolOption(config.Options, "disableSSL"),
			DisableACL:   boolOption(config.Options, "disable_acl") || boolOption(config.Options, "disableACL"),
		}
		if s3Config.Bucket == "" {
			s3Config.Bucket = config.Root
		}
		s3Config.AccessKeyID = firstNonEmpty(stringOption(config.Options, "access_key_id"), stringOption(config.Options, "accessKeyID"))
		s3Config.AccessKeySecret = firstNonEmpty(stringOption(config.Options, "access_key_secret"), stringOption(config.Options, "accessKeySecret"))
		s3Config.SessionToken = firstNonEmpty(stringOption(config.Options, "session_token"), stringOption(config.Options, "sessionToken"))
		return New(ctx, s3Config)
	}
}

func (c Config) withDefaults() (Config, error) {
	if c.Bucket == "" {
		return c, fmt.Errorf("%w: s3 bucket is required", filesystem.ErrInvalidPath)
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.Visibility == "" {
		c.Visibility = filesystem.VisibilityDefault
	}
	if !c.Visibility.Valid() {
		return c, filesystem.ErrInvalidVisibility
	}
	if c.DisableACL && c.Visibility == filesystem.VisibilityPublic {
		return c, filesystem.ErrUnsupported
	}
	return c, nil
}

type unsupportedPresignClient struct{}

func (unsupportedPresignClient) PresignGetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	return nil, filesystem.ErrUnsupported
}

func applyS3CompatibilityOptions(options *awss3.Options) {
	options.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	options.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
}

func objectACL(visibility filesystem.Visibility) types.ObjectCannedACL {
	if visibility == filesystem.VisibilityPublic {
		return types.ObjectCannedACLPublicRead
	}
	if visibility == filesystem.VisibilityDefault {
		return ""
	}
	return types.ObjectCannedACLPrivate
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

func withScheme(endpoint string, disableSSL bool) string {
	if endpoint == "" || hasScheme(endpoint) {
		return endpoint
	}
	if disableSSL {
		return "http://" + endpoint
	}
	return "https://" + endpoint
}

func hasScheme(endpoint string) bool {
	return len(endpoint) >= 7 && (endpoint[:7] == "http://" || (len(endpoint) >= 8 && endpoint[:8] == "https://"))
}
