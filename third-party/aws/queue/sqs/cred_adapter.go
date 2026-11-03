package sqs

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/kitsunium/sdk/internal/core/observe/logger/writer"
)

// credAdapter bridges writer.CredentialProvider onto the AWS SDK's
// aws.CredentialsProvider, so every signature re-reads the caller's provider
// and a rotation is picked up without rebuilding the client — the writers
// adapter, repeated here because the writer's is unexported.
type credAdapter struct {
	provider writer.CredentialProvider
}

// Retrieve returns the provider's current credentials in the AWS SDK's shape.
func (a credAdapter) Retrieve(ctx context.Context) (creds aws.Credentials, err error) {
	cv, cerr := a.provider.Credentials(ctx)
	//: a provider failure fails the signature, and so the request.
	if cerr != nil {
		//: the cause as the provider gave it; the caller wraps the request.
		return aws.Credentials{}, cerr
	}
	//: the three halves, never logged.
	return aws.Credentials{
		AccessKeyID:     cv.AccessKeyID(),
		SecretAccessKey: cv.SecretAccessKey(),
		SessionToken:    cv.SessionToken(),
		Source:          "kitsunium/queue-sqs",
	}, nil
}
