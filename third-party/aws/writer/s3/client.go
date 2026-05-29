// Package s3 — the AWS adapter. This file (with cred_adapter.go) is the ONLY
// place that imports the AWS SDK; every other file is SDK-free and unit-tested
// through the uploadFunc seam. The live PutObject lives in an anonymous closure
// returned by newUploadFunc — confining the SDK here and keeping the batching
// logic testable without a network.
package s3

import (
	"bytes"
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// newUploadFunc builds the AWS-backed upload seam from cfg, adapting its
// credentials into the SDK's interface. A nil cfg.Credentials is rejected
// (explicit credentials required for v1). A non-empty cfg.Endpoint overrides the
// S3 endpoint (path-style) — for S3-compatible backends or a LocalStack test.
// The optional opts are applied last and exist so tests can inject smithy stub
// middleware (Options.APIOptions) to exercise this closure without a network.
// The returned closure issues one PutObject per batch and is the sole live SDK
// call site.
func newUploadFunc(cfg writer.S3Config, opts ...func(*awss3.Options)) (up uploadFunc, err error) {
	//: v1 requires explicit credentials; refuse the nil default-chain path.
	if cfg.Credentials == nil {
		//: surface the typed client-init sentinel (the factory's contract).
		return nil, ClientInitFailed
	}
	//: build a static AWS config from region + the adapted credential source.
	awsCfg := aws.Config{Region: cfg.Region, Credentials: credAdapter{provider: cfg.Credentials}}
	//: construct the S3 client once and capture it in the upload closure.
	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		//: a custom endpoint targets S3-compatible / LocalStack backends.
		if cfg.Endpoint != "" {
			//: path-style is required for most non-AWS S3 endpoints.
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
		//: apply test-supplied option hooks (e.g. stub middleware) last.
		for _, opt := range opts {
			opt(o)
		}
	})
	//: the closure is the only live PutObject site — no named, untestable method.
	return func(ctx context.Context, key string, body []byte) error {
		//: PutObject with the batch bytes as the object body.
		_, perr := client.PutObject(ctx, &awss3.PutObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(key),
			Body:   bytes.NewReader(body),
		})
		//: hand the SDK error back to the sink, which wraps it as PutFailed.
		return perr
	}, nil
}
