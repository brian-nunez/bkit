package s3

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/brian-nunez/bkit/objex"
)

const (
	defaultRegion      = "us-east-1"
	minPartSize        = 5 << 20
	maxPartSize        = 5 << 30
	defaultPartSize    = 8 << 20
	defaultConcurrency = 4
	maxParts           = 10000
)

// api is the subset of *s3.Client the driver uses.
type api interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, opts ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	CopyObject(ctx context.Context, in *s3.CopyObjectInput, opts ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(ctx context.Context, in *s3.UploadPartInput, opts ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	UploadPartCopy(ctx context.Context, in *s3.UploadPartCopyInput, opts ...func(*s3.Options)) (*s3.UploadPartCopyOutput, error)
	CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, opts ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, opts ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	CreateBucket(ctx context.Context, in *s3.CreateBucketInput, opts ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	DeleteBucket(ctx context.Context, in *s3.DeleteBucketInput, opts ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
	ListBuckets(ctx context.Context, in *s3.ListBucketsInput, opts ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	HeadBucket(ctx context.Context, in *s3.HeadBucketInput, opts ...func(*s3.Options)) (*s3.HeadBucketOutput, error)
}

// presignAPI is the subset of *s3.PresignClient the driver uses.
type presignAPI interface {
	PresignGetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// settings collects Option values.
type settings struct {
	region      string
	endpoint    string
	pathStyle   bool
	credentials aws.CredentialsProvider
	awsConfig   *aws.Config
	partSize    int64
	concurrency int
}

// Option configures a Client.
type Option func(*settings) error

// WithRegion sets the AWS region. Without it the region comes from the
// SDK's configuration chain, falling back to us-east-1. S3-compatible
// services that ignore regions accept any value (R2 uses "auto").
func WithRegion(region string) Option {
	return func(s *settings) error {
		if region == "" {
			return fmt.Errorf("%w: region must not be empty", objex.ErrInvalidOption)
		}
		s.region = region
		return nil
	}
}

// WithEndpoint sends requests to an S3-compatible service instead of AWS,
// for example "http://localhost:9000" for MinIO or
// "https://<account>.r2.cloudflarestorage.com" for R2. With a custom
// endpoint, request checksums are sent only when an operation requires
// them, which every S3-compatible service accepts.
func WithEndpoint(endpoint string) Option {
	return func(s *settings) error {
		u, err := url.Parse(endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%w: endpoint %q must be an absolute http or https URL", objex.ErrInvalidOption, endpoint)
		}
		s.endpoint = strings.TrimSuffix(endpoint, "/")
		return nil
	}
}

// WithPathStyle addresses buckets as https://host/bucket instead of
// https://bucket.host. Most self-hosted S3-compatible services need it.
func WithPathStyle() Option {
	return func(s *settings) error {
		s.pathStyle = true
		return nil
	}
}

// WithCredentials uses static credentials instead of the SDK's default
// credential chain. sessionToken may be empty.
func WithCredentials(accessKeyID, secretAccessKey, sessionToken string) Option {
	return func(s *settings) error {
		if accessKeyID == "" || secretAccessKey == "" {
			return fmt.Errorf("%w: access key ID and secret access key must not be empty", objex.ErrInvalidOption)
		}
		s.credentials = credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, sessionToken)
		return nil
	}
}

// WithAWSConfig uses cfg instead of loading the SDK's default
// configuration, for applications that already build an aws.Config.
// WithRegion and WithCredentials still override cfg's values.
func WithAWSConfig(cfg aws.Config) Option {
	return func(s *settings) error {
		s.awsConfig = &cfg
		return nil
	}
}

// WithPartSize sets the multipart upload part size in bytes, between 5 MiB
// and 5 GiB. Bodies up to one part are sent with a single request. It
// defaults to 8 MiB. Each concurrent part is buffered in memory.
func WithPartSize(bytes int64) Option {
	return func(s *settings) error {
		if bytes < minPartSize || bytes > maxPartSize {
			return fmt.Errorf("%w: part size must be between 5 MiB and 5 GiB", objex.ErrInvalidOption)
		}
		s.partSize = bytes
		return nil
	}
}

// WithUploadConcurrency sets how many parts of one multipart upload are sent
// at once. It defaults to 4.
func WithUploadConcurrency(n int) Option {
	return func(s *settings) error {
		if n < 1 {
			return fmt.Errorf("%w: upload concurrency must be at least 1", objex.ErrInvalidOption)
		}
		s.concurrency = n
		return nil
	}
}

// Client talks to an S3 or S3-compatible service. It implements
// objex.Manager.
type Client struct {
	api         api
	presign     presignAPI
	region      string
	endpoint    string
	partSize    int64
	concurrency int
	maxParts    int32
}

var _ objex.Manager = (*Client)(nil)

// New returns a Client. It loads the SDK's default configuration unless
// WithAWSConfig is given; it does not contact the service.
func New(ctx context.Context, opts ...Option) (*Client, error) {
	s := settings{partSize: defaultPartSize, concurrency: defaultConcurrency}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&s); err != nil {
			return nil, objex.NewError("open", "", "", objex.ErrInvalidOption, err)
		}
	}

	var cfg aws.Config
	if s.awsConfig != nil {
		cfg = s.awsConfig.Copy()
	} else {
		var loadOpts []func(*config.LoadOptions) error
		if s.region != "" {
			loadOpts = append(loadOpts, config.WithRegion(s.region))
		}
		if s.credentials != nil {
			loadOpts = append(loadOpts, config.WithCredentialsProvider(s.credentials))
		}
		loaded, err := config.LoadDefaultConfig(ctx, loadOpts...)
		if err != nil {
			return nil, objex.NewError("open", "", "", nil, err)
		}
		cfg = loaded
	}
	if s.region != "" {
		cfg.Region = s.region
	}
	if cfg.Region == "" {
		cfg.Region = defaultRegion
	}
	if s.credentials != nil {
		cfg.Credentials = s.credentials
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = s.pathStyle
		if s.endpoint != "" {
			o.BaseEndpoint = aws.String(s.endpoint)
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})
	return newClient(client, s3.NewPresignClient(client), cfg.Region, s), nil
}

func newClient(a api, p presignAPI, region string, s settings) *Client {
	return &Client{
		api:         a,
		presign:     p,
		region:      region,
		endpoint:    s.endpoint,
		partSize:    s.partSize,
		concurrency: s.concurrency,
		maxParts:    maxParts,
	}
}

// Bucket returns a handle to the named bucket. It performs no I/O.
func (c *Client) Bucket(name string) *Bucket {
	return &Bucket{client: c, name: name}
}

// CreateBucket creates a bucket in the client's region.
func (c *Client) CreateBucket(ctx context.Context, name string) error {
	const op = "create bucket"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return err
	}
	in := &s3.CreateBucketInput{Bucket: aws.String(name)}
	// AWS rejects an explicit us-east-1 constraint, and S3-compatible
	// services define their own location rules, so only AWS regions other
	// than us-east-1 get one.
	if c.endpoint == "" && c.region != defaultRegion {
		in.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(c.region),
		}
	}
	if _, err := c.api.CreateBucket(ctx, in); err != nil {
		return wrap(op, name, "", err)
	}
	return nil
}

// DeleteBucket deletes an empty bucket.
func (c *Client) DeleteBucket(ctx context.Context, name string) error {
	const op = "delete bucket"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return err
	}
	if _, err := c.api.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)}); err != nil {
		return wrap(op, name, "", err)
	}
	return nil
}

// ListBuckets returns every bucket the credentials can see, sorted by name.
func (c *Client) ListBuckets(ctx context.Context) ([]objex.BucketInfo, error) {
	const op = "list buckets"
	var buckets []objex.BucketInfo
	in := &s3.ListBucketsInput{}
	for {
		out, err := c.api.ListBuckets(ctx, in)
		if err != nil {
			return nil, wrap(op, "", "", err)
		}
		for _, b := range out.Buckets {
			buckets = append(buckets, objex.BucketInfo{
				Name:      aws.ToString(b.Name),
				CreatedAt: aws.ToTime(b.CreationDate).UTC(),
			})
		}
		if aws.ToString(out.ContinuationToken) == "" {
			break
		}
		in.ContinuationToken = out.ContinuationToken
	}
	slices.SortFunc(buckets, func(a, b objex.BucketInfo) int { return strings.Compare(a.Name, b.Name) })
	return buckets, nil
}

// BucketExists reports whether the bucket exists and is accessible.
func (c *Client) BucketExists(ctx context.Context, name string) (bool, error) {
	const op = "bucket exists"
	if err := checkBucketCall(ctx, op, name); err != nil {
		return false, err
	}
	_, err := c.api.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
	if err == nil {
		return true, nil
	}
	wrapped := wrap(op, name, "", err)
	if errors.Is(wrapped, objex.ErrNotFound) || errors.Is(wrapped, objex.ErrBucketNotFound) {
		return false, nil
	}
	return false, wrapped
}

func checkBucketCall(ctx context.Context, op, name string) error {
	if err := ctx.Err(); err != nil {
		return objex.NewError(op, name, "", nil, err)
	}
	if err := objex.ValidateBucketName(name); err != nil {
		return objex.NewError(op, name, "", objex.ErrInvalidBucketName, err)
	}
	return nil
}
