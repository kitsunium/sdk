package cloudwatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/smithy-go/middleware"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
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

// cwRecorder is a real in-process CloudWatch Logs endpoint: it records every
// PutLogEvents request body the production AWS client POSTs and replies with the
// canned JSON success the SDK needs to decode a PutLogEventsOutput. It is the
// stdlib-server seam the E2E drives the whole writer chain against — no mocked
// deliverFunc, no smithy stub: the bytes asserted are the bytes that crossed the
// socket.
type cwRecorder struct {
	mu     sync.Mutex
	bodies [][]byte
}

func (r *cwRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	//: capture the raw request body for byte-level assertions.
	body, rerr := io.ReadAll(req.Body)
	//: a read failure would silently drop the request from the record, so 500
	//: surfaces it to the SDK (and thus the test) instead of a false success.
	if rerr != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	r.mu.Lock()
	r.bodies = append(r.bodies, body)
	r.mu.Unlock()
	//: AWS JSON 1.1 — an empty object decodes into a zero PutLogEventsOutput.
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	//: the SDK needs a body to decode; a write failure here just fails the round
	//: trip, which the test's Close error check would catch.
	if _, werr := io.WriteString(w, "{}"); werr != nil {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (r *cwRecorder) messages() []cloudwatchlogs.PutLogEventsInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]cloudwatchlogs.PutLogEventsInput, 0, len(r.bodies))
	for _, b := range r.bodies {
		//: decode each recorded wire body back into the typed input the writer sent.
		var in cloudwatchlogs.PutLogEventsInput
		if json.Unmarshal(b, &in) == nil {
			out = append(out, in)
		}
	}
	return out
}

func TestWriteEndToEndOverRealServer(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		line string
	}
	tests := []tc{
		{"a Write reaches the server as a real PutLogEvents request", "hello-from-e2e\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &cwRecorder{}
		//: a real HTTP server stands in for the CloudWatch Logs endpoint.
		srv := httptest.NewServer(rec)
		t.Cleanup(srv.Close)
		//: build the FULL production chain (levelgate(async(cwSink))) via the
		//: registry, with Endpoint pointed at the in-process server — the only
		//: knob a test turns; everything below is production code.
		cfg := writer.CloudWatchConfig{
			Group: "e2e-grp", Stream: "e2e-strm", Region: "eu-west-3",
			Endpoint: srv.URL, Credentials: fakeProvider{},
		}
		sink, err := writer.Open("cloudwatch", cfg)
		if err != nil {
			t.Fatalf("%s: Open: %v", c.name, err)
		}
		//: a recent timestamp clears the V85 14d/2h window filter so the event
		//: survives deliverBatch and reaches the server (truncated to ms, the unit
		//: the wire carries, so the round-trip assertion is exact).
		recTime := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
		//: drive the producer-facing Write end-to-end through async + batcher.
		if _, werr := sink.Write(t.Context(), corelogger.RecordEvent{Time: recTime}, []byte(c.line)); werr != nil {
			t.Fatalf("%s: Write: %v", c.name, werr)
		}
		//: Close drains the async ring, flushes the batch, and joins — forcing the
		//: real PutLogEvents round-trip to complete before assertions.
		if cerr := sink.Close(); cerr != nil {
			t.Fatalf("%s: Close: %v", c.name, cerr)
		}
		msgs := rec.messages()
		//: exactly one PutLogEvents request must have crossed the socket.
		if len(msgs) != 1 {
			t.Fatalf("%s: server saw %d PutLogEvents requests, want 1", c.name, len(msgs))
		}
		got := msgs[0]
		//: the group/stream and the single event's message must round-trip the wire
		//: exactly as the producer wrote them.
		if aws.ToString(got.LogGroupName) != "e2e-grp" || aws.ToString(got.LogStreamName) != "e2e-strm" ||
			len(got.LogEvents) != 1 || aws.ToString(got.LogEvents[0].Message) != c.line {
			t.Errorf("%s: server received group=%q stream=%q events=%d msg=%q",
				c.name, aws.ToString(got.LogGroupName), aws.ToString(got.LogStreamName),
				len(got.LogEvents), aws.ToString(got.LogEvents[0].Message))
		}
		//: the source timestamp must survive the full chain as epoch milliseconds.
		want := recTime.UnixMilli()
		if len(got.LogEvents) == 1 && (got.LogEvents[0].Timestamp == nil || *got.LogEvents[0].Timestamp != want) {
			t.Errorf("%s: server received timestamp=%v want %d", c.name, got.LogEvents[0].Timestamp, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_newDeliverFunc_Endpoint(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		endpoint string
	}
	tests := []tc{
		{"a non-empty Endpoint overrides the resolved BaseEndpoint", "http://localhost:4566"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: capture the Options the construction-time hook ran against; the closure
		//: in client.go applies cfg.Endpoint BEFORE the test opts, so by the time
		//: this hook runs BaseEndpoint is already set when Endpoint is non-empty.
		var gotEndpoint string
		cfg := writer.CloudWatchConfig{Group: "g", Stream: "s", Region: "eu-west-3", Endpoint: c.endpoint, Credentials: fakeProvider{}}
		_, err := newDeliverFunc(cfg, func(o *cloudwatchlogs.Options) {
			//: record the override the cfg.Endpoint branch installed on the client.
			if o.BaseEndpoint != nil {
				gotEndpoint = *o.BaseEndpoint
			}
		})
		if err != nil {
			t.Fatalf("%s: newDeliverFunc: %v", c.name, err)
		}
		//: the cfg.Endpoint branch must have set BaseEndpoint to the override URL.
		if gotEndpoint != c.endpoint {
			t.Errorf("%s: BaseEndpoint=%q want %q", c.name, gotEndpoint, c.endpoint)
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
		//: the first event's millisecond timestamp must round-trip too, proving the
		//: closure maps every event's ts (not just the last) via UnixMilli.
		want0 := time.Unix(1700000000, 0).UnixMilli()
		if captured.LogEvents[0].Timestamp == nil || *captured.LogEvents[0].Timestamp != want0 {
			t.Errorf("%s: first event timestamp=%v want %d", c.name, captured.LogEvents[0].Timestamp, want0)
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
