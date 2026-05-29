// Package writer — S3Config value type for the "s3" writer.
package writer

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// S3Config configures the "s3" writer (registered by third-party/aws/writer/s3). It
// carries no AWS types — only plain data — so it lives in the dep-light core
// layer and is re-exported as logger.S3Config. Bucket and Region are required;
// the factory rejects an empty Bucket/Region with WriterConfigInvalid.
type S3Config struct {
	// Bucket is the destination S3 bucket name (required).
	Bucket string
	// Region is the AWS region of the bucket (required).
	Region string
	// Prefix is an optional key prefix prepended to every uploaded object.
	Prefix string
	// Endpoint optionally overrides the S3 endpoint URL (path-style). Empty uses
	// the default AWS endpoint; set it for S3-compatible backends (MinIO,
	// GovCloud) or a LocalStack integration test.
	Endpoint string
	// Credentials yields short-lived AWS credentials on demand and is REQUIRED:
	// a nil provider is rejected with ClientInitFailed. The AWS default
	// credential chain is intentionally not wired (it would pull the heavyweight
	// aws config module into every consumer).
	Credentials CredentialProvider
	// FlushEvery bounds how long a batch waits before upload; the zero value
	// uploads only on an explicit Flush / Close.
	FlushEvery time.Duration
	// MaxBatchBytes caps the in-memory batch before a forced upload; the zero
	// value applies the factory's default.
	MaxBatchBytes int
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking buffer saturated. Wire it to a metric counter;
	// the hot path never blocks on a slow upload, so drops are how
	// back-pressure surfaces.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed batch upload seen by
	// the background drainer. Without it those errors are lost (the producer
	// cannot be blocked on a network failure).
	OnError func(err error)
}
