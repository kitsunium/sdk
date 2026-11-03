package s3

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"testing"
	"time"

	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// statusError is a response error carrying only an HTTP status, as a HEAD
// answers: no body, so no error code.
func statusError(status int) error {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New("no body"),
	}
}

func TestEveryS3FailureIsTypedUnderThePackageRangeAndKeepsItsCause(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cause error
		name  string
		want  errs.Code
	}{
		{&smithy.GenericAPIError{Code: "NoSuchBucket"}, "a missing bucket", CodeS3FSBucketMissing},
		{&smithy.GenericAPIError{Code: "AccessDenied"}, "access denied", CodeS3FSAccessDenied},
		{statusError(http.StatusForbidden), "a bare 403", CodeS3FSAccessDenied},
		{&smithy.GenericAPIError{Code: "SlowDown"}, "slow down", CodeS3FSThrottled},
		{statusError(http.StatusServiceUnavailable), "a bare 503", CodeS3FSThrottled},
		{&smithy.GenericAPIError{Code: "InternalError"}, "anything else", CodeS3FSRequestFailed},
		{errors.New("connection reset"), "a transport failure", CodeS3FSRequestFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bucket := newFakeBucket()
			filesystem := newFS(Config{Bucket: "b", MaxKeys: 10, Timeout: time.Minute}, bucket)
			bucket.fail(tc.cause)
			_, openErr := filesystem.Open("file.txt")
			if !errs.HasCode(openErr, tc.want) || !errs.HasCode(openErr, corevfs.CodeReadFailed) {
				t.Errorf("Open = %v, want %s with READ_FAILED in its trail", openErr, tc.want)
			}
			if !errors.Is(openErr, tc.cause) {
				t.Errorf("Open = %v, want the vendor's cause kept in the chain", openErr)
			}
			if errors.Is(openErr, fs.ErrNotExist) {
				t.Errorf("Open = %v, a failed request must not read as an absent file", openErr)
			}
			writeErr := filesystem.WriteAtomic("file.txt", []byte("x"), 0o644)
			if !errs.HasCode(writeErr, tc.want) {
				t.Errorf("WriteAtomic = %v, want %s", writeErr, tc.want)
			}
		})
	}
}

func TestAFailedPublicationIsPublishFailedAndSafeToRetry(t *testing.T) {
	t.Parallel()
	bucket := newFakeBucket()
	filesystem := newFS(Config{Bucket: "b", MaxKeys: 10, Timeout: time.Minute}, bucket)
	if err := filesystem.WriteAtomic("page.html", []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteAtomic = %v, want nil", err)
	}
	failing := &failingPut{fakeBucket: bucket, err: &smithy.GenericAPIError{Code: "InternalError"}}
	filesystem.api = failing
	err := filesystem.WriteAtomic("page.html", []byte("new"), 0o644)
	if !errs.HasCode(err, corevfs.CodePublishFailed) {
		t.Fatalf("WriteAtomic = %v, want PUBLISH_FAILED", err)
	}
	filesystem.api = bucket
	got, readErr := fs.ReadFile(filesystem, "page.html")
	if readErr != nil || string(got) != "old" {
		t.Fatalf("after a failed publication = (%q, %v), want the previous bytes", got, readErr)
	}
}

func TestAnObjectAboveTheSinglePutLimitIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Parallel()
	bucket := newFakeBucket()
	filesystem := newFS(Config{Bucket: "b", MaxKeys: 10, Timeout: time.Minute}, bucket)
	filesystem.maxObject = 4
	for _, write := range []func() error{
		func() error { return filesystem.WriteFile("big", []byte("12345"), 0o644) },
		func() error { return filesystem.WriteAtomic("big", []byte("12345"), 0o644) },
	} {
		if err := write(); !errs.HasCode(err, CodeS3FSObjectTooLarge) {
			t.Fatalf("write = %v, want OBJECT_TOO_LARGE", err)
		}
	}
	if bucket.puts != 0 {
		t.Fatalf("%d PutObject sent, want none", bucket.puts)
	}
}

func TestAListingLargerThanAPageIsReadPageByPage(t *testing.T) {
	t.Parallel()
	filesystem := newFS(Config{Bucket: "b", MaxKeys: 2, Timeout: time.Minute}, newFakeBucket())
	names := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt"}
	for _, name := range names {
		mustWrite(t, filesystem, name, name)
	}
	if err := filesystem.MkdirAll("f/g", 0o755); err != nil {
		t.Fatalf("MkdirAll = %v", err)
	}
	entries, err := filesystem.ReadDir(".")
	if err != nil || len(entries) != len(names)+1 {
		t.Fatalf("ReadDir = (%d entries, %v), want %d", len(entries), err, len(names)+1)
	}
	if !entries[len(names)].IsDir() || entries[len(names)].Name() != "f" {
		t.Fatalf("last entry = %v, want the directory f", entries[len(names)])
	}
}

// failingPut is a bucket whose PutObject fails.
type failingPut struct {
	*fakeBucket
	err error
}

func (f *failingPut) PutObject(_ context.Context, _ *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	return nil, f.err
}
