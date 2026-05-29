//go:build localstack

// Package s3 — LocalStack integration test (L3). Excluded from the default
// build/`bazel test //...` by the `localstack` build tag; it needs a running
// LocalStack container emulating S3:
//
//	docker run --rm -p 4566:4566 localstack/localstack
//	GOWORK=off go test -tags localstack ./third-party/aws/writer/s3/...
//
// LOCALSTACK_ENDPOINT overrides the endpoint (default http://localhost:4566).
// It exercises the REAL PutObject against the emulator and reads the object
// back — the end-to-end check the hermetic stub test cannot give.
package s3

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// localCreds is a static CredentialProvider with the canned LocalStack keys.
type localCreds struct{}

func (localCreds) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: LocalStack accepts any non-empty credentials; "test"/"test" is the convention.
	return writer.NewCredentialValue("test", "test", ""), nil
}

func localstackEndpoint() string {
	//: allow CI to point at a non-default LocalStack host:port.
	if e := os.Getenv("LOCALSTACK_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:4566"
}

func TestLocalStackPutObject(t *testing.T) {
	endpoint := localstackEndpoint()
	const bucket, region, key = "kitsunium-it", "eu-west-3", "logs/it.log"
	ctx := t.Context()

	//: a raw client (same endpoint) creates the bucket and reads the object back.
	raw := awss3.NewFromConfig(
		aws.Config{Region: region, Credentials: credAdapter{provider: localCreds{}}},
		func(o *awss3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true },
	)
	if _, err := raw.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("CreateBucket: %v (is LocalStack running at %s?)", err, endpoint)
	}

	//: the production seam uploads through the real PutObject path to LocalStack.
	up, err := newUploadFunc(writer.S3Config{Bucket: bucket, Region: region, Endpoint: endpoint, Credentials: localCreds{}})
	if err != nil {
		t.Fatalf("newUploadFunc: %v", err)
	}
	if uerr := up(ctx, key, []byte("e2e-payload\n")); uerr != nil {
		t.Fatalf("upload: %v", uerr)
	}

	//: read the object back and assert the batch landed verbatim.
	out, err := raw.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	defer func() { _ = out.Body.Close() }()
	body, _ := io.ReadAll(out.Body)
	if !bytes.Equal(body, []byte("e2e-payload\n")) {
		t.Errorf("object body = %q, want %q", string(body), "e2e-payload\n")
	}
}
