// Package sse_test — the public facade, from a consumer's side of the module
// boundary.
package sse_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/server"
	"github.com/kitsunium/sdk/pkg/v1/server/sse"
)

// TestTheShortestUsefulStream is the facade's own acceptance test: everything a
// consumer needs to hold an HTTP response open and push events down it, with
// nothing imported from the SDK but this package.
//
// It is deliberately measured in statements. If a future change makes this grow
// — a config type to build, an encoder to install, an error to thread before
// the first event — the surface has regressed in the dimension it exists for.
func TestTheShortestUsefulStream(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stream, err := sse.New(w, r)
		if err != nil {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		defer closeOrFail(t, stream)
		for i := range 3 {
			if serr := stream.Send(sse.Event{ID: string(rune('a' + i)), Data: "tick"}); serr != nil {
				return
			}
		}
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if got := rec.Header().Get("Content-Type"); got != sse.ContentType {
		t.Fatalf("Content-Type = %q, want %q", got, sse.ContentType)
	}
	want := "id: a\ndata: tick\n\nid: b\ndata: tick\n\nid: c\ndata: tick\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

// TestSentinelsAreMatchable pins that a consumer can branch on a failure
// without string matching. The aliases must be the SAME sentinel values the
// engine returns, or errors.Is would quietly answer false across the module
// boundary — which is the whole failure mode a re-exported sentinel exists to
// prevent.
func TestSentinelsAreMatchable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: provoke returns the failure under test.
		provoke func(t *testing.T) error
		want    error
	}
	tests := []tc{
		{
			name: "a response that cannot be flushed",
			provoke: func(t *testing.T) error {
				t.Helper()
				_, err := sse.New(&unflushable{header: make(http.Header)},
					httptest.NewRequest(http.MethodGet, "/stream", nil))
				return err
			},
			want: sse.FlushUnsupported,
		},
		{
			name: "an option the domain will not interpret",
			provoke: func(t *testing.T) error {
				t.Helper()
				_, err := sse.New(httptest.NewRecorder(),
					httptest.NewRequest(http.MethodGet, "/stream", nil),
					sse.KeepAlive(-time.Second))
				return err
			},
			want: sse.StreamMisconfigured,
		},
		{
			name: "an event the format cannot carry",
			provoke: func(t *testing.T) error {
				t.Helper()
				stream := open(t)
				return stream.Send(sse.Event{ID: "a\nb", Data: "x"})
			},
			want: sse.FieldInvalid,
		},
		{
			name: "a send after the stream ended",
			provoke: func(t *testing.T) error {
				t.Helper()
				stream := open(t)
				if err := stream.Close(); err != nil {
					t.Fatalf("Close() = %v", err)
				}
				return stream.Send(sse.Event{Data: "late"})
			},
			want: sse.StreamClosed,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if err := c.provoke(t); !errors.Is(err, c.want) {
			t.Fatalf("got %v, want it to match %v", err, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDrainSignalIsReadableFromTheFacade pins the mechanism an event stream is
// only the first consumer of: a long poll, or anything built on a protocol
// upgrade, holds a connection open the same way and needs the same warning.
func TestDrainSignalIsReadableFromTheFacade(t *testing.T) {
	t.Parallel()
	//: a server that publishes no signal hands back nil, which blocks forever
	//: in a select — so a caller needs no nil check.
	if got := sse.DrainSignal(context.Background()); got != nil {
		t.Fatalf("DrainSignal() = %v on a bare context, want nil", got)
	}
	//: the same accessor is re-exported from the server facade, so a handler
	//: that does not stream need not import this package to observe a drain.
	if got := server.DrainSignal(context.Background()); got != nil {
		t.Fatalf("server.DrainSignal() = %v on a bare context, want nil", got)
	}
}

// TestKeepAliveIsNeverSilentlyNever pins ADR 0031 at the public edge: zero is
// clamped to a working cadence, never read as "off". A stream with keep-alive
// silently disabled works on a developer's loopback and dies at one minute
// behind a real proxy.
func TestKeepAliveIsNeverSilentlyNever(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	stream, err := sse.New(rec, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.KeepAlive(0))
	if err != nil {
		t.Fatalf("New() = %v, want a clamped keep-alive rather than a refusal", err)
	}
	defer closeOrFail(t, stream)
	if sse.DefaultKeepAlive <= 0 {
		t.Fatalf("DefaultKeepAlive = %v, want a positive cadence", sse.DefaultKeepAlive)
	}
	//: and a negative interval is refused rather than clamped, because no value
	//: the SDK invented for it would be defensible.
	if _, nerr := sse.New(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/s", nil),
		sse.KeepAlive(-time.Second)); !errors.Is(nerr, sse.StreamMisconfigured) {
		t.Fatalf("New(KeepAlive(-1s)) = %v, want STREAM_MISCONFIGURED", nerr)
	}
}

// TestRetryBelowTheWireResolutionIsRefused pins that a sub-millisecond hint is
// not rounded to zero. "retry: 0" does not mean "very soon", it tells the
// client to reconnect immediately — a hot loop aimed at a server that is
// probably already struggling.
func TestRetryBelowTheWireResolutionIsRefused(t *testing.T) {
	t.Parallel()
	_, err := sse.New(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil),
		sse.Retry(sse.MinRetry-1))
	if !errors.Is(err, sse.FieldInvalid) {
		t.Fatalf("New(Retry(sub-ms)) = %v, want FIELD_INVALID", err)
	}
}

// TestMultiLinePayloadSplitsRatherThanEscapes pins the format's one surprising
// property at the public edge: a newline is not escaped, it becomes another
// data line, and the client rejoins them with "\n".
func TestMultiLinePayloadSplitsRatherThanEscapes(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	stream, err := sse.New(rec, httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	defer closeOrFail(t, stream)
	if serr := stream.Send(sse.Event{Data: "line one\nline two"}); serr != nil {
		t.Fatalf("Send() = %v, want nil", serr)
	}
	if got := rec.Body.String(); got != "data: line one\ndata: line two\n\n" {
		t.Fatalf("body = %q", got)
	}
	if strings.Contains(rec.Body.String(), `\n`) {
		t.Fatalf("the payload was escaped rather than split: %q", rec.Body.String())
	}
}

// closeOrFail closes a stream in a defer, failing the test if it reports a
// problem.
func closeOrFail(t *testing.T, closer io.Closer) {
	t.Helper()
	if err := closer.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// open builds a stream on a recorder, for the cases that only care about what
// the stream refuses.
func open(t *testing.T) *sse.Stream {
	t.Helper()
	stream, err := sse.New(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/stream", nil), sse.WithoutKeepAlive())
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if cerr := stream.Close(); cerr != nil {
			t.Errorf("Close() = %v", cerr)
		}
	})
	return stream
}

// unflushable is a ResponseWriter that cannot stream — the shape the
// constructor exists to refuse before it writes anything.
type unflushable struct {
	header http.Header
}

// Header implements http.ResponseWriter.
func (u *unflushable) Header() http.Header {
	//: the header map, so the refusal can be shown to leave it untouched.
	return u.header
}

// Write implements http.ResponseWriter.
func (u *unflushable) Write(p []byte) (int, error) {
	//: accepted and discarded; this writer exists only to lack Flush.
	return len(p), nil
}

// WriteHeader implements http.ResponseWriter.
func (u *unflushable) WriteHeader(int) {}
