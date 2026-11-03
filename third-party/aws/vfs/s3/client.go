package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

// objectAPI is the part of the S3 client the filesystem calls. It is a seam:
// the production value is *awss3.Client, and the package's tests run every
// verb over an in-memory bucket that answers as S3 answers.
type objectAPI interface {
	HeadObject(ctx context.Context, in *awss3.HeadObjectInput, opts ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
	GetObject(ctx context.Context, in *awss3.GetObjectInput, opts ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *awss3.PutObjectInput, opts ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	DeleteObject(ctx context.Context, in *awss3.DeleteObjectInput, opts ...func(*awss3.Options)) (*awss3.DeleteObjectOutput, error)
	DeleteObjects(ctx context.Context, in *awss3.DeleteObjectsInput, opts ...func(*awss3.Options)) (*awss3.DeleteObjectsOutput, error)
	ListObjectsV2(ctx context.Context, in *awss3.ListObjectsV2Input, opts ...func(*awss3.Options)) (*awss3.ListObjectsV2Output, error)
}

// newClient builds the S3 client of cfg. It sends nothing: the AWS SDK
// connects on the first request.
func newClient(cfg *Config) *awss3.Client {
	awsCfg := aws.Config{Region: cfg.Region, Credentials: credAdapter{provider: cfg.Credentials}}
	//: an endpoint override is an emulator or an S3-compatible store, which
	//: name buckets in the path rather than in the host.
	return awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		//: the region's endpoint unless one is named.
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})
}
