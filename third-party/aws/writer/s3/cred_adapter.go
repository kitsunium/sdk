// Package s3 — bridges the SDK's writer.CredentialProvider onto the AWS SDK's
// aws.CredentialsProvider so credentials refresh on each Retrieve. Split from
// client.go to keep one struct per file.
package s3

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// credAdapter adapts a writer.CredentialProvider into aws.CredentialsProvider.
type credAdapter struct {
	// provider is the SDK-side credential source supplied in the config.
	provider writer.CredentialProvider
}

// Retrieve satisfies aws.CredentialsProvider, translating a CredentialValue
// into the AWS credential shape on each call (so rotated credentials are
// picked up).
func (a credAdapter) Retrieve(ctx context.Context) (creds aws.Credentials, err error) {
	//: pull the current credential set from the consumer's provider.
	cv, cerr := a.provider.Credentials(ctx)
	//: propagate a provider failure so the SDK surfaces it on the call.
	if cerr != nil {
		//: empty credentials + the cause; the SDK reports the request failure.
		return aws.Credentials{}, cerr
	}
	//: map our redacting value onto the SDK's plain credential struct.
	return aws.Credentials{
		AccessKeyID:     cv.AccessKeyID(),
		SecretAccessKey: cv.SecretAccessKey(),
		SessionToken:    cv.SessionToken(),
		Source:          "kitsunium/awswriters",
	}, nil
}
