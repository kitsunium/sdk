// Package writer — CloudWatchConfig value type for the "cloudwatch" writer.
package writer

import (
	"time"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

// CloudWatchConfig configures the "cloudwatch" writer (registered by
// third-party/aws/writer/cloudwatch). Like S3Config it carries no AWS types and lives
// in core, re-exported as logger.CloudWatchConfig. Group, Stream and Region are
// required; the factory rejects empties with WriterConfigInvalid.
type CloudWatchConfig struct {
	// Group is the CloudWatch Logs log-group name (required).
	Group string
	// Stream is the log-stream name within the group (required).
	Stream string
	// Region is the AWS region of the log group (required).
	Region string
	// Endpoint optionally overrides the CloudWatch Logs endpoint URL. Empty uses
	// the default AWS endpoint; set it for a LocalStack integration test.
	Endpoint string
	// Credentials yields short-lived AWS credentials on demand and is REQUIRED:
	// a nil provider is rejected with ClientInitFailed. The AWS default
	// credential chain is intentionally not wired (it would pull the heavyweight
	// aws config module into every consumer).
	Credentials CredentialProvider
	// FlushEvery bounds how long a batch waits before the PutLogEvents call;
	// the zero value flushes only on an explicit Flush / Close.
	FlushEvery time.Duration
	// MinLevel is the optional per-writer severity floor; the zero value
	// inherits the handler-global level.
	MinLevel level.Level
	// OnDrop, when non-nil, is invoked with the count of records discarded
	// because the non-blocking buffer saturated. Wire it to a metric counter.
	OnDrop func(dropped int)
	// OnError, when non-nil, is invoked for every failed PutLogEvents call seen
	// by the background drainer.
	OnError func(err error)
}
