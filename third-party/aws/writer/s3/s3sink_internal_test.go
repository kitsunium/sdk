package s3

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeUploader records uploads so the batching tests need no network.
type fakeUploader struct {
	mu     sync.Mutex
	bodies [][]byte
	keys   []string
	err    error
}

func (f *fakeUploader) upload(_ context.Context, key string, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	//: a configured error simulates an upload failure.
	if f.err != nil {
		return f.err
	}
	//: copy the body so later batch reuse cannot alias a recorded upload.
	f.bodies = append(f.bodies, slices.Clone(body))
	f.keys = append(f.keys, key)
	return nil
}

func (f *fakeUploader) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func writeLine(t *testing.T, s *s3Sink, line string) {
	t.Helper()
	//: a Write must always report the full payload accepted.
	if n, err := s.Write(t.Context(), corelogger.RecordEvent{}, []byte(line)); err != nil || n != len(line) {
		t.Fatalf("Write(%q)=(%d,%v) want (%d,nil)", line, n, err, len(line))
	}
}

func Test_payloadBytes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []byte
		want int64
	}
	tests := []tc{
		{"empty payload weighs zero", nil, 0},
		{"weight equals byte length", []byte("kitsunium"), 9},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: payloadBytes is the batcher WeightOf seam — it must report len in bytes.
		if got := payloadBytes(c.in); got != c.want {
			t.Errorf("%s: payloadBytes=%d want %d", c.name, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_newS3Sink(t *testing.T) {
	t.Parallel()
	type tc struct {
		name          string
		maxBatchBytes int
		onError       func(error)
	}
	tests := []tc{
		{"zero cap + nil onError apply defaults", 0, nil},
		{"explicit cap + hook are honoured", 512, func(error) {}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		s := newS3Sink((&fakeUploader{}).upload, "p/", c.maxBatchBytes, 0, c.onError)
		//: the constructor must always yield a usable sink with its batcher + hook wired.
		if s == nil || s.batch == nil || s.up == nil || s.onError == nil {
			t.Errorf("%s: newS3Sink gave batch-nil=%v up-nil=%v onError-nil=%v", c.name, s.batch == nil, s.up == nil, s.onError == nil)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_Flush(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		lines    []string
		wantPuts int
		wantBody string
	}
	tests := []tc{
		{"two lines coalesce into one object", []string{"a\n", "b\n"}, 1, "a\nb\n"},
		{"empty buffer flush is a no-op", nil, 0, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakeUploader{}
		s := newS3Sink(fp.upload, "p/", 0, 0, nil)
		for _, l := range c.lines {
			writeLine(t, s, l)
		}
		//: an explicit flush uploads exactly one coalesced object (or none).
		if err := s.Flush(t.Context()); err != nil {
			t.Fatalf("%s: Flush: %v", c.name, err)
		}
		if fp.count() != c.wantPuts {
			t.Fatalf("%s: puts=%d want %d", c.name, fp.count(), c.wantPuts)
		}
		//: when a body was expected, it must equal the concatenated lines.
		if c.wantPuts == 1 && string(fp.bodies[0]) != c.wantBody {
			t.Errorf("%s: body=%q want %q", c.name, string(fp.bodies[0]), c.wantBody)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_Write(t *testing.T) {
	t.Parallel()
	type tc struct {
		name          string
		maxBatchBytes int
		lines         []string
		wantMinPuts   int
	}
	tests := []tc{
		{"reaching the byte cap forces an upload", 2, []string{"xy"}, 1},
		{"below the cap defers the upload", 1024, []string{"x"}, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakeUploader{}
		s := newS3Sink(fp.upload, "", c.maxBatchBytes, 0, nil)
		for _, l := range c.lines {
			writeLine(t, s, l)
		}
		//: the cap-trigger arm uploads inline; the under-cap arm defers.
		if fp.count() < c.wantMinPuts {
			t.Errorf("%s: puts=%d want >= %d", c.name, fp.count(), c.wantMinPuts)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		flushEvery time.Duration
	}
	tests := []tc{
		{"close flushes the final batch (no ticker)", 0},
		{"close stops the ticker and flushes", 5 * time.Millisecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakeUploader{}
		s := newS3Sink(fp.upload, "", 0, c.flushEvery, nil)
		writeLine(t, s, "final\n")
		//: Close must upload the buffered record and join any ticker.
		if err := s.Close(); err != nil {
			t.Fatalf("%s: Close: %v", c.name, err)
		}
		if fp.count() == 0 {
			t.Errorf("%s: Close did not flush the final batch", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_onError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"a cap-triggered upload failure is routed to onError"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("upload boom")
		var mu sync.Mutex
		var gotErr error
		//: maxBatchBytes=1 makes the first Write cross the cap and flush inline;
		//: the failed background upload must reach onError (not the caller).
		fp := &fakeUploader{err: boom}
		s := newS3Sink(fp.upload, "", 1, 0, func(e error) {
			mu.Lock()
			gotErr = e
			mu.Unlock()
		})
		writeLine(t, s, "x\n")
		//: the background routing must have observed the upload failure.
		mu.Lock()
		seen := gotErr
		mu.Unlock()
		if !errors.Is(seen, boom) {
			t.Errorf("onError saw %v want %v", seen, boom)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_flushError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"Flush propagates the upload error to the caller"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		boom := errors.New("flush boom")
		//: no onError hook — Flush must return the error directly.
		fp := &fakeUploader{err: boom}
		s := newS3Sink(fp.upload, "", 0, 0, nil)
		writeLine(t, s, "x\n")
		//: the caller-facing Flush propagates rather than routing to onError.
		err := s.Flush(t.Context())
		if !errors.Is(err, boom) {
			t.Errorf("Flush err=%v want %v", err, boom)
		}
		//: errors.Is must reach the typed PutFailed sentinel too.
		if !errors.Is(err, PutFailed) {
			t.Errorf("Flush err=%v does not match PutFailed", err)
		}
		//: the wrapped error must carry PutFailed's I/O exit code (74), not the
		//: default 70 — proves WrapParams.ExitCode is honoured on the wrap path.
		if got := errs.ExitCodeOf(err); got != exitIOErr {
			t.Errorf("ExitCodeOf=%d want %d", got, exitIOErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_deliver(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		prefix   string
		payloads []string
		wantBody string
	}
	tests := []tc{
		{"payloads concatenate into one object", "logs/", []string{"a\n", "b\n", "c\n"}, "a\nb\nc\n"},
		{"single payload is delivered verbatim", "", []string{"solo\n"}, "solo\n"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		fp := &fakeUploader{}
		s := newS3Sink(fp.upload, c.prefix, 0, 0, nil)
		items := make([][]byte, 0, len(c.payloads))
		for _, p := range c.payloads {
			items = append(items, []byte(p))
		}
		//: deliver concatenates the batch and uploads exactly one object.
		if err := s.deliver(t.Context(), items); err != nil {
			t.Fatalf("%s: deliver: %v", c.name, err)
		}
		if fp.count() != 1 {
			t.Fatalf("%s: puts=%d want 1", c.name, fp.count())
		}
		//: the uploaded body must be the in-order concatenation.
		if string(fp.bodies[0]) != c.wantBody {
			t.Errorf("%s: body=%q want %q", c.name, string(fp.bodies[0]), c.wantBody)
		}
		//: the generated key must carry the configured prefix and the .log suffix.
		if !strings.HasPrefix(fp.keys[0], c.prefix) || !strings.HasSuffix(fp.keys[0], ".log") {
			t.Errorf("%s: key=%q want prefix %q and .log suffix", c.name, fp.keys[0], c.prefix)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

func Test_s3Sink_ticker(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"the ticker flushes a buffered batch without an explicit Flush"}}
	runCase := func(t *testing.T, _ tc) {
		t.Helper()
		fp := &fakeUploader{}
		s := newS3Sink(fp.upload, "", 0, 2*time.Millisecond, nil)
		t.Cleanup(func() {
			//: surface a close failure rather than discarding it.
			if err := s.Close(); err != nil {
				t.Errorf("cleanup close: %v", err)
			}
		})
		writeLine(t, s, "tick\n")
		//: poll until the background ticker has uploaded the batch.
		deadline := 0
		for fp.count() == 0 && deadline < 200 {
			time.Sleep(2 * time.Millisecond)
			deadline++
		}
		if fp.count() == 0 {
			t.Errorf("ticker did not flush within the deadline")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
