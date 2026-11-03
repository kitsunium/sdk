package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
)

// queueAPI is the part of the SQS client the broker calls. It is a seam: the
// production value is *awssqs.Client, and the package's tests run every verb
// over an in-memory queue that answers as SQS answers, on a ManualClock.
type queueAPI interface {
	SendMessage(ctx context.Context, in *awssqs.SendMessageInput, opts ...func(*awssqs.Options)) (*awssqs.SendMessageOutput, error)
	ReceiveMessage(ctx context.Context, in *awssqs.ReceiveMessageInput, opts ...func(*awssqs.Options)) (*awssqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, in *awssqs.DeleteMessageInput, opts ...func(*awssqs.Options)) (*awssqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *awssqs.ChangeMessageVisibilityInput, opts ...func(*awssqs.Options)) (*awssqs.ChangeMessageVisibilityOutput, error)
}

// newClient builds the SQS client of cfg. It sends nothing: the AWS SDK
// connects on the first request.
func newClient(cfg *Config) *awssqs.Client {
	awsCfg := aws.Config{Region: cfg.Region, Credentials: credAdapter{provider: cfg.Credentials}}
	//: an endpoint override is LocalStack, ElasticMQ or a VPC endpoint.
	return awssqs.NewFromConfig(awsCfg, func(o *awssqs.Options) {
		//: the region's endpoint unless one is named.
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
}
