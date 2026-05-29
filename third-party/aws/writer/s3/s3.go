// Package s3 registers the "s3" writer factory. It lives under third-party/* in
// the root umbrella module — NOT in pkg/v1 — so the AWS SDK it pulls never
// enters the module graph of pkg/v1 consumers (nothing requires the root
// module); see ADR 0012. Blank-importing the package self-registers the factory
// (and pulls the AWS SDK), so writer.Open("s3", logger.S3Config{…}) resolves
// only in builds that opt in. The factory wraps the batching terminal sink
// (s3sink.go) with the async middleware for a non-blocking ring + OnDrop, and
// with levelgate for the per-writer MinLevel.
package s3

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered s3 factory singleton (no init(); package-level var).
var Writer = writer.Register(&s3Factory{})

// s3Factory builds the S3 writer chain from a writer.S3Config.
type s3Factory struct{}

// Name reports the canonical key "s3".
func (*s3Factory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "s3"
}

// Open validates the config, builds the AWS-backed batching sink, and wraps it
// for non-blocking delivery + per-writer level filtering. A wrong-type config
// or a missing Bucket/Region returns WriterConfigInvalid; a client-init failure
// returns ClientInitFailed.
func (*s3Factory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	c, ok := cfg.(writer.S3Config)
	//: reject a wrong-type config or a missing Bucket/Region with one guard;
	//: !ok short-circuits before the zero-value c fields are read.
	if !ok || c.Bucket == "" || c.Region == "" {
		//: surface the shared config-type/required-field sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the AWS-backed upload seam (client.go owns the SDK details).
	up, pErr := newUploadFunc(c)
	//: a client-init failure aborts construction with a typed wrap.
	if pErr != nil {
		//: wrap the SDK cause so errors.Is still reaches it.
		return nil, errs.Wrap(pErr, errs.WrapParams{
			Code:    CodeS3ClientInitFailed,
			Reason:  "CLIENT_INIT_FAILED",
			Public:  "S3 writer could not initialise its AWS client",
			Private: "third-party/aws/writer/s3.Open: newUploadFunc failed",
		})
	}
	//: terminal batching sink → async (non-block + OnDrop) → levelgate (floor).
	base := newS3Sink(up, c.Prefix, c.MaxBatchBytes, c.FlushEvery, c.OnError)
	nonblocking := async.New(base, async.Config{OnDrop: c.OnDrop})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, c.MinLevel), nil
}
