package http1_test

import (
	"bytes"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// traceHex is a valid version-00 traceparent's trace id.
const (
	traceHex = "0af7651916cd43dd8448eb211c80319c"
	spanHex  = "b7ad6b7169203331"
	validTP  = "00-" + traceHex + "-" + spanHex + "-01"
)

// withTrace returns a request carrying a traceparent field of value tp.
func withTrace(tp string) string {
	return "GET / HTTP/1.1\r\nHost: h\r\ntraceparent: " + tp + "\r\n\r\n"
}

// traceSink records what the handler saw.
type traceSink struct {
	valid   bool
	traceID [16]byte
	served  int
	defers  bool
	pending []*corenet.HTTP1ResponseValue
}

func (s *traceSink) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	s.served++
	s.valid = req.Trace.IsValid()
	s.traceID = req.Trace.TraceID
	res.Status = 200
	if s.defers {
		res.Defer = true
		s.pending = append(s.pending, res)
	}
}

func (s *traceSink) EndBatch(int) {
	for _, res := range s.pending {
		res.Defer = false
	}
	s.pending = s.pending[:0]
}

// serveThroughTrace runs one request through the Trace link behind the adapter.
func serveThroughTrace(sink *traceSink, in string) (string, *fakeIO) {
	a := http1.HandleLoop(http1.Trace()(sink), http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte(in))
	a.OnBatchEnd(io)
	return string(io.out[ref]), io
}

// TestTrace_readsAValidTraceparent pins the first acceptance of #855: a valid
// field reaches the handler as a valid context with the same trace id, and the
// answer names that trace.
func TestTrace_readsAValidTraceparent(t *testing.T) {
	t.Parallel()
	sink := &traceSink{}
	got, _ := serveThroughTrace(sink, withTrace(validTP))
	if !sink.valid {
		t.Fatal("a valid traceparent reached the handler invalid")
	}
	var want [16]byte
	for i := range want {
		want[i] = hexPair(traceHex[2*i:])
	}
	if sink.traceID != want {
		t.Fatalf("trace id = %x, want %s", sink.traceID, traceHex)
	}
	if !strings.Contains(got, "traceparent: "+validTP+"\r\n") {
		t.Fatalf("the answer does not name the trace: %q", got)
	}
}

// hexPair decodes one lowercase hex pair.
func hexPair(s string) byte {
	nib := func(c byte) byte {
		if c >= 'a' {
			return c - 'a' + 10
		}
		return c - '0'
	}
	return nib(s[0])<<4 | nib(s[1])
}

// TestTrace_malformedIsLeftInvalidAndServed pins the second: a bad version, a
// zero trace id and a value of 54 bytes each leave the context invalid and the
// request served, with no field on the answer.
func TestTrace_malformedIsLeftInvalidAndServed(t *testing.T) {
	t.Parallel()
	malformed := []string{
		"ff-" + traceHex + "-" + spanHex + "-01",
		"00-" + strings.Repeat("0", 32) + "-" + spanHex + "-01",
		validTP[:54],
		"",
		"garbage",
		"00-" + strings.ToUpper(traceHex) + "-" + spanHex + "-01",
	}
	for _, tp := range malformed {
		sink := &traceSink{}
		got, _ := serveThroughTrace(sink, withTrace(tp))
		if sink.served != 1 || sink.valid {
			t.Errorf("%q: served %d, valid %v, want served once and invalid", tp, sink.served, sink.valid)
		}
		if strings.Contains(got, "traceparent") {
			t.Errorf("%q: the answer carries a traceparent: %q", tp, got)
		}
	}
}

// TestTrace_noFieldIsNoTrace pins the common case: no field, no value, no line.
func TestTrace_noFieldIsNoTrace(t *testing.T) {
	t.Parallel()
	sink := &traceSink{}
	if got, _ := serveThroughTrace(sink, "GET / HTTP/1.1\r\nHost: h\r\n\r\n"); strings.Contains(got, "traceparent") || sink.valid || sink.served != 1 {
		t.Fatalf("no field: %q, valid %v", got, sink.valid)
	}
}

// TestTrace_aDeferredAnswerKeepsItsField pins that the field line outlives the
// request value: a deferred answer, written after the next request was framed
// into the same request value, still names its own trace.
func TestTrace_aDeferredAnswerKeepsItsField(t *testing.T) {
	t.Parallel()
	sink := &traceSink{defers: true}
	got, _ := serveThroughTrace(sink, withTrace(validTP)+"GET /plain HTTP/1.1\r\nHost: h\r\n\r\n")
	if !strings.Contains(got, "traceparent: "+validTP+"\r\n") {
		t.Fatalf("the deferred answer lost its field: %q", got)
	}
	if strings.Count(got, "HTTP/1.1 200") != 2 {
		t.Fatalf("answers = %q", got)
	}
}

// recordingLog keeps the last access record.
type recordingLog struct{ rec corenet.HTTP1AccessValue }

func (l *recordingLog) LogHTTP1(rec corenet.HTTP1AccessValue) { l.rec = rec }

// TestTrace_theAccessRecordCarriesTheTraceID pins the access record: Log behind
// Trace records the trace id, and nothing without the field.
func TestTrace_theAccessRecordCarriesTheTraceID(t *testing.T) {
	t.Parallel()
	rec := &recordingLog{}
	logLink, err := http1.Log(rec)
	if err != nil {
		t.Fatal(err)
	}
	h := http1.Trace()(logLink(&traceSink{}))
	a := http1.HandleLoop(h, http1.Parser{})
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	a.OnData(io, ref, []byte(withTrace(validTP)))
	a.OnBatchEnd(io)
	if !rec.rec.TraceID.IsValid() {
		t.Fatalf("the record carries no trace id: %+v", rec.rec)
	}
	if !bytes.Equal(rec.rec.TraceID[:], func() []byte {
		var w [16]byte
		for i := range w {
			w[i] = hexPair(traceHex[2*i:])
		}
		return w[:]
	}()) {
		t.Fatalf("record trace id = %x", rec.rec.TraceID)
	}
}

// TestTrace_fieldLengthsZeroToSixtyFour pins, deterministically, what the
// fuzz target's seeds explore: a traceparent field of every length 0..64 is
// parsed or left invalid, the request served either way, and is never a panic.
func TestTrace_fieldLengthsZeroToSixtyFour(t *testing.T) {
	t.Parallel()
	for n := 0; n <= 64; n++ {
		value := strings.Repeat("a", n)
		if n >= len(validTP) {
			value = validTP + value[len(validTP):]
		}
		sink := &traceSink{}
		serveThroughTrace(sink, withTrace(value))
		if sink.served != 1 {
			t.Fatalf("length %d: served %d times", n, sink.served)
		}
		if sink.valid && n != len(validTP) {
			t.Fatalf("length %d: read as a valid context", n)
		}
	}
}
