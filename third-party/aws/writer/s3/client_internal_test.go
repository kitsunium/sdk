package s3

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// fakeProvider is a CredentialProvider that returns fixed credentials, or an
// error when err is set. Shared by the s3 factory + client tests.
type fakeProvider struct{ err error }

func (f fakeProvider) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: a configured error simulates an unreachable credential source.
	if f.err != nil {
		return writer.CredentialValue{}, f.err
	}
	//: otherwise hand back fixed SigV4 material.
	return writer.NewCredentialValue("AKIATEST", "secret", ""), nil
}

func Test_newUploadFunc(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		creds   writer.CredentialProvider
		wantErr bool
	}
	tests := []tc{
		{"nil credentials are rejected", nil, true},
		{"valid credentials build an uploader offline", fakeProvider{}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		up, err := newUploadFunc(writer.S3Config{Bucket: "bucket", Region: "eu-west-3", Credentials: c.creds})
		//: failure arm — error + nil uploader (no AWS client built).
		if c.wantErr {
			if err == nil || up != nil {
				t.Errorf("%s: err=%v up=%v want error+nil", c.name, err, up)
			}
			return
		}
		//: happy arm — NewFromConfig builds a client without any network call.
		if err != nil || up == nil {
			t.Errorf("%s: err=%v up=%v want nil+uploader", c.name, err, up)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stubPutObject returns an s3 client option that injects a smithy Initialize
// middleware capturing the PutObjectInput and short-circuiting with retErr (or
// a canned success). This is the AWS SDK's recommended in-process test seam: it
// exercises the real client.PutObject entry path — the part fakes can't reach —
// without any network.
func stubPutObject(capture *awss3.PutObjectInput, retErr error) func(*awss3.Options) {
	return func(o *awss3.Options) {
		//: APIOptions lets a test add middleware to the operation stack.
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			//: Initialize sees the typed input; short-circuiting avoids the network.
			return stack.Initialize.Add(
				middleware.InitializeMiddlewareFunc("stubPutObject",
					func(_ context.Context, in middleware.InitializeInput, _ middleware.InitializeHandler) (out middleware.InitializeOutput, md middleware.Metadata, err error) {
						//: capture the input the closure built for assertions.
						if params, ok := in.Parameters.(*awss3.PutObjectInput); ok {
							*capture = *params
						}
						//: simulate an upload failure when the test asked for one.
						if retErr != nil {
							return out, md, retErr
						}
						//: canned success short-circuits the rest of the stack.
						out.Result = &awss3.PutObjectOutput{}
						return out, md, nil
					}),
				middleware.Before,
			)
		})
	}
}

func TestUploadFuncContract(t *testing.T) {
	t.Parallel()
	boom := errors.New("put boom")
	type tc struct {
		name    string
		retErr  error
		wantErr bool
	}
	tests := []tc{
		{"success builds the right PutObject and returns nil", nil, false},
		{"SDK error propagates from the closure", boom, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var captured awss3.PutObjectInput
		cfg := writer.S3Config{Bucket: "my-bucket", Region: "eu-west-3", Credentials: fakeProvider{}}
		//: real closure, real client.PutObject path, stub middleware (no network).
		up, err := newUploadFunc(cfg, stubPutObject(&captured, c.retErr))
		if err != nil {
			t.Fatalf("%s: newUploadFunc: %v", c.name, err)
		}
		uerr := up(t.Context(), "objkey", []byte("hello\n"))
		//: the error arm must surface the SDK failure through the closure.
		if c.wantErr {
			if !errors.Is(uerr, boom) {
				t.Errorf("%s: upload err=%v want wrap of %v", c.name, uerr, boom)
			}
			return
		}
		//: the success arm must report no error...
		if uerr != nil {
			t.Fatalf("%s: upload: %v", c.name, uerr)
		}
		//: ...and the closure must have built bucket/key/body correctly.
		body, rerr := io.ReadAll(captured.Body)
		if rerr != nil {
			t.Fatalf("%s: reading captured body: %v", c.name, rerr)
		}
		gotBucket, gotKey, gotBody := aws.ToString(captured.Bucket), aws.ToString(captured.Key), string(body)
		if gotBucket != "my-bucket" || gotKey != "objkey" || gotBody != "hello\n" {
			t.Errorf("%s: PutObject built bucket=%q key=%q body=%q", c.name, gotBucket, gotKey, gotBody)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
