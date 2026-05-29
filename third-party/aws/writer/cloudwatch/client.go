// Package cloudwatch — the AWS adapter. This file (with cred_adapter.go) is the
// ONLY place that imports the AWS SDK; every other file is SDK-free and unit-
// tested through the deliverFunc seam. The live PutLogEvents lives in an
// anonymous closure returned by newDeliverFunc.
package cloudwatch

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// errCredentialsRequired is returned when no CredentialProvider was supplied;
// the v1 CloudWatch writer requires explicit credentials.
var errCredentialsRequired = errors.New("cloudwatch writer requires an explicit CredentialProvider")

// newDeliverFunc builds the AWS-backed delivery seam from cfg, adapting its
// credentials into the SDK's interface. A nil cfg.Credentials is rejected. A
// non-empty cfg.Endpoint overrides the CloudWatch Logs endpoint (for a
// LocalStack test). The optional opts are applied last so tests can inject
// smithy stub middleware to exercise this closure without a network. The
// returned closure issues one PutLogEvents per batch and is the sole live SDK
// call site.
func newDeliverFunc(cfg writer.CloudWatchConfig, opts ...func(*cloudwatchlogs.Options)) (deliver deliverFunc, err error) {
	//: v1 requires explicit credentials; refuse the nil default-chain path.
	if cfg.Credentials == nil {
		//: surface the documented requirement to the factory.
		return nil, errCredentialsRequired
	}
	//: build a static AWS config from region + the adapted credential source.
	awsCfg := aws.Config{Region: cfg.Region, Credentials: credAdapter{provider: cfg.Credentials}}
	//: construct the client once and capture it in the delivery closure.
	client := cloudwatchlogs.NewFromConfig(awsCfg, func(o *cloudwatchlogs.Options) {
		//: a custom endpoint targets a LocalStack backend.
		if cfg.Endpoint != "" {
			//: override the resolved CloudWatch Logs endpoint URL.
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		//: apply test-supplied option hooks (e.g. stub middleware) last.
		for _, opt := range opts {
			opt(o)
		}
	})
	//: the closure is the only live PutLogEvents site — no named, untestable method.
	return func(ctx context.Context, events []cwEvent) error {
		//: translate the SDK-free events into the CloudWatch input shape.
		in := make([]cwtypes.InputLogEvent, len(events))
		//: each event carries its source timestamp in epoch milliseconds.
		for i, e := range events {
			//: PutLogEvents requires *string message + *int64 ms timestamp.
			in[i] = cwtypes.InputLogEvent{Message: aws.String(e.msg), Timestamp: aws.Int64(e.ts.UnixMilli())}
		}
		//: deliver the whole batch in one PutLogEvents call.
		_, perr := client.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
			LogGroupName:  aws.String(cfg.Group),
			LogStreamName: aws.String(cfg.Stream),
			LogEvents:     in,
		})
		//: hand the SDK error back to the sink, which wraps it as PutFailed.
		return perr
	}, nil
}
