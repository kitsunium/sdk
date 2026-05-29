package cloudwatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go/middleware"

	"github.com/kitsunium/sdk/internal/core/writer"
)

// fakeProvider returns fixed credentials, or an error when err is set.
type fakeProvider struct{ err error }

func (f fakeProvider) Credentials(_ context.Context) (writer.CredentialValue, error) {
	//: a configured error simulates an unreachable credential source.
	if f.err != nil {
		return writer.CredentialValue{}, f.err
	}
	//: otherwise hand back fixed SigV4 material.
	return writer.NewCredentialValue("AKIATEST", "secret", ""), nil
}

func Test_newDeliverFunc(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		creds   writer.CredentialProvider
		wantErr bool
	}
	tests := []tc{
		{"nil credentials are rejected", nil, true},
		{"valid credentials build a putter offline", fakeProvider{}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		deliver, err := newDeliverFunc(writer.CloudWatchConfig{Group: "group", Stream: "stream", Region: "eu-west-3", Credentials: c.creds})
		//: failure arm — error + nil deliver seam (no AWS client built).
		if c.wantErr {
			if err == nil || deliver != nil {
				t.Errorf("%s: err=%v deliver-nil=%v want error+non-nil", c.name, err, deliver == nil)
			}
			return
		}
		//: happy arm — NewFromConfig builds a client without any network call.
		if err != nil || deliver == nil {
			t.Errorf("%s: err=%v deliver-nil=%v want nil+seam", c.name, err, deliver == nil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// stubPutLogEvents returns a CloudWatch Logs client option that injects a smithy
// Initialize middleware capturing the PutLogEventsInput and short-circuiting
// with retErr (or a canned success). AWS-recommended in-process test seam: it
// exercises the real client.PutLogEvents entry path without a network.
func stubPutLogEvents(capture *cloudwatchlogs.PutLogEventsInput, retErr error) func(*cloudwatchlogs.Options) {
	return func(o *cloudwatchlogs.Options) {
		//: APIOptions lets a test add middleware to the operation stack.
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			//: Initialize sees the typed input; short-circuiting avoids the network.
			return stack.Initialize.Add(
				middleware.InitializeMiddlewareFunc("stubPutLogEvents",
					func(_ context.Context, in middleware.InitializeInput, _ middleware.InitializeHandler) (out middleware.InitializeOutput, md middleware.Metadata, err error) {
						//: capture the input the closure built for assertions.
						if params, ok := in.Parameters.(*cloudwatchlogs.PutLogEventsInput); ok {
							*capture = *params
						}
						//: simulate a delivery failure when the test asked for one.
						if retErr != nil {
							return out, md, retErr
						}
						//: canned success short-circuits the rest of the stack.
						out.Result = &cloudwatchlogs.PutLogEventsOutput{}
						return out, md, nil
					}),
				middleware.Before,
			)
		})
	}
}

func TestDeliverFuncContract(t *testing.T) {
	t.Parallel()
	boom := errors.New("putlogevents boom")
	type tc struct {
		name    string
		retErr  error
		wantErr bool
	}
	tests := []tc{
		{"success builds the right PutLogEvents and returns nil", nil, false},
		{"SDK error propagates from the closure", boom, true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var captured cloudwatchlogs.PutLogEventsInput
		cfg := writer.CloudWatchConfig{Group: "grp", Stream: "strm", Region: "eu-west-3", Credentials: fakeProvider{}}
		//: real closure, real client.PutLogEvents path, stub middleware (no network).
		deliver, err := newDeliverFunc(cfg, stubPutLogEvents(&captured, c.retErr))
		if err != nil {
			t.Fatalf("%s: newDeliverFunc: %v", c.name, err)
		}
		events := []cwEvent{
			{ts: time.Unix(1700000000, 0), msg: "first\n"},
			{ts: time.Unix(1700000001, 0), msg: "second\n"},
		}
		derr := deliver(t.Context(), events)
		//: the error arm must surface the SDK failure through the closure.
		if c.wantErr {
			if !errors.Is(derr, boom) {
				t.Errorf("%s: deliver err=%v want wrap of %v", c.name, derr, boom)
			}
			return
		}
		//: the success arm must report no error...
		if derr != nil {
			t.Fatalf("%s: deliver: %v", c.name, derr)
		}
		//: ...and the closure must map group/stream + every event message.
		if aws.ToString(captured.LogGroupName) != "grp" || aws.ToString(captured.LogStreamName) != "strm" || len(captured.LogEvents) != 2 {
			t.Fatalf("%s: built group=%q stream=%q events=%d", c.name,
				aws.ToString(captured.LogGroupName), aws.ToString(captured.LogStreamName), len(captured.LogEvents))
		}
		//: the second event's message + millisecond timestamp must round-trip.
		want := time.Unix(1700000001, 0).UnixMilli()
		if aws.ToString(captured.LogEvents[1].Message) != "second\n" ||
			captured.LogEvents[1].Timestamp == nil || *captured.LogEvents[1].Timestamp != want {
			t.Errorf("%s: second event mismatch: %+v", c.name, captured.LogEvents[1])
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
