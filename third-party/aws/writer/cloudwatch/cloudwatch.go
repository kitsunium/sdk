// Package cloudwatch registers the "cloudwatch" writer factory. It lives under
// third-party/* in the root umbrella module — NOT in pkg/v1 — so the AWS SDK it
// pulls never enters the module graph of pkg/v1 consumers (ADR 0012).
// Blank-importing the package self-registers the factory (and pulls the AWS
// SDK), so writer.Open("cloudwatch",
// logger.CloudWatchConfig{…}) resolves only in builds that opt in. The factory
// wraps the batching terminal sink (cwsink.go) with async (non-blocking ring +
// OnDrop) and levelgate (per-writer MinLevel).
package cloudwatch

import (
	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/core/writer"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/logger/middleware/async"
	"github.com/kitsunium/sdk/internal/service/writer/levelgate"
)

// Writer is the registered cloudwatch factory singleton (no init()).
var Writer = writer.Register(&cwFactory{})

// cwFactory builds the CloudWatch writer chain from a writer.CloudWatchConfig.
type cwFactory struct{}

// Name reports the canonical key "cloudwatch".
func (*cwFactory) Name() writer.Name {
	//: the literal key consumers pass in a WriterSpec.
	return "cloudwatch"
}

// Open validates the config, builds the AWS-backed batching sink, and wraps it
// for non-blocking delivery + per-writer level filtering. A wrong-type config
// or a missing Group/Stream/Region returns WriterConfigInvalid; a client-init
// failure returns ClientInitFailed.
func (*cwFactory) Open(cfg writer.Config) (sink corelogger.Sink, err error) {
	c, ok := cfg.(writer.CloudWatchConfig)
	//: reject a wrong-type config or any missing required field with one guard;
	//: !ok short-circuits before the zero-value c fields are read.
	if !ok || c.Group == "" || c.Stream == "" || c.Region == "" {
		//: surface the shared config-type/required-field sentinel.
		return nil, writer.WriterConfigInvalid
	}
	//: build the AWS-backed delivery seam (client.go owns the SDK details).
	deliver, pErr := newDeliverFunc(c)
	//: a client-init failure aborts construction with a typed wrap.
	if pErr != nil {
		//: wrap the SDK cause so errors.Is still reaches it.
		return nil, errs.Wrap(pErr, errs.WrapParams{
			Code:    CodeCWClientInitFailed,
			Reason:  "CLIENT_INIT_FAILED",
			Public:  "CloudWatch writer could not initialise its AWS client",
			Private: "third-party/aws/writer/cloudwatch.Open: newDeliverFunc failed",
		})
	}
	//: terminal batching sink → async (non-block + OnDrop) → levelgate (floor).
	base := newCWSink(deliver, 0, c.FlushEvery, c.OnError)
	nonblocking := async.New(base, async.Config{OnDrop: c.OnDrop})
	//: outermost gate drops below-floor records before they reach the ring.
	return levelgate.New(nonblocking, c.MinLevel), nil
}
