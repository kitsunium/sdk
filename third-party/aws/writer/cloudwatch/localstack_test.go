//go:build localstack

// Package cloudwatch — LocalStack integration test (L3). Excluded from the
// default build/`bazel test //...` by the `localstack` build tag; it needs a
// running LocalStack container emulating CloudWatch Logs:
//
//	docker run --rm -p 4566:4566 localstack/localstack
//	GOWORK=off go test -tags localstack ./third-party/aws/writer/cloudwatch/...
//
// LOCALSTACK_ENDPOINT overrides the endpoint (default http://localhost:4566).
// It exercises the REAL PutLogEvents against the emulator and reads the events
// back — the end-to-end check the hermetic stub test cannot give.
package cloudwatch

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// localCreds is a static CredentialProvider with the canned LocalStack keys.
type localCreds struct{}

func (localCreds) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: LocalStack accepts any non-empty credentials; "test"/"test" is the convention.
	return writer.NewCredentialValue("test", "test", ""), nil
}

func localstackEndpoint() string {
	//: allow CI to point at a non-default LocalStack host:port.
	if e := os.Getenv("LOCALSTACK_ENDPOINT"); e != "" {
		return e
	}
	return "http://localhost:4566"
}

func TestLocalStackPutLogEvents(t *testing.T) {
	endpoint := localstackEndpoint()
	const group, stream, region = "kitsunium-it", "it-stream", "eu-west-3"
	ctx := t.Context()

	//: a raw client (same endpoint) provisions the group/stream and reads back.
	raw := cloudwatchlogs.NewFromConfig(
		aws.Config{Region: region, Credentials: credAdapter{provider: localCreds{}}},
		func(o *cloudwatchlogs.Options) { o.BaseEndpoint = aws.String(endpoint) },
	)
	if _, err := raw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)}); err != nil {
		t.Fatalf("CreateLogGroup: %v (is LocalStack running at %s?)", err, endpoint)
	}
	if _, err := raw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	}); err != nil {
		t.Fatalf("CreateLogStream: %v", err)
	}

	//: the production seam delivers through the real PutLogEvents path.
	deliver, err := newDeliverFunc(writer.CloudWatchConfig{Group: group, Stream: stream, Region: region, Endpoint: endpoint, Credentials: localCreds{}})
	if err != nil {
		t.Fatalf("newDeliverFunc: %v", err)
	}
	if derr := deliver(ctx, []cwEvent{{ts: time.Now(), msg: "e2e-event\n"}}); derr != nil {
		t.Fatalf("deliver: %v", derr)
	}

	//: read the events back and assert the message landed.
	out, err := raw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream),
	})
	if err != nil {
		t.Fatalf("GetLogEvents: %v", err)
	}
	found := false
	for _, e := range out.Events {
		//: scan the returned events for the message the writer delivered.
		if e.Message != nil && *e.Message == "e2e-event\n" {
			found = true
		}
	}
	if !found {
		t.Errorf("delivered event not found in GetLogEvents output (%d events)", len(out.Events))
	}
}
