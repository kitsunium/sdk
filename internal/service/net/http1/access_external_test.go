package http1_test

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// accessH advances the clock by took during ServeHTTP1, for "/defer" holds the
// answer and completes it in EndBatch, late after being held.
type accessH struct {
	clk  *clock.ManualClock
	took time.Duration
	late time.Duration
	held []*corenet.HTTP1ResponseValue
}

func (h *accessH) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	h.clk.Advance(h.took)
	if string(req.Path) == "/defer" {
		res.Defer = true
		h.held = append(h.held, res)
		return
	}
	res.Body = []byte("now")
}

func (h *accessH) EndBatch(int) {
	h.clk.Advance(h.late)
	for _, res := range h.held {
		res.Defer, res.Status, res.Body = false, 201, []byte("later")
	}
	h.held = h.held[:0]
}

// accessChain is a Log link over h on the adapter, with its options and clock.
func accessChain(t *testing.T, h *accessH, rec *accessRecorder, opts ...http1.LogOption) *http1.LoopHandler {
	t.Helper()
	logLink, err := http1.Log(rec, append(opts, http1.LogClock(h.clk))...)
	if err != nil {
		t.Fatal(err)
	}
	return http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](h, logLink), http1.Parser{})
}

// oneBatch sends one request on a new connection, in its own batch.
func oneBatch(t *testing.T, a *http1.LoopHandler, io *fakeIO, slot uint32, path string) {
	t.Helper()
	ref := corenet.LoopConnRef{Slot: slot, Gen: 1}
	a.OnOpen(io, ref)
	raw := "GET " + path + " HTTP/1.1\r\nHost: h\r\n\r\n"
	if n := a.OnData(io, ref, []byte(raw)); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(io)
}

// TestLog_slowerThanLogsOnlyTheSlow is the acceptance: a deferred answer
// completed 2 ms later is one record with its status and elapsed time, a
// 100 µs answer none.
func TestLog_slowerThanLogsOnlyTheSlow(t *testing.T) {
	h := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0)), took: 100 * time.Microsecond, late: 2 * time.Millisecond}
	rec := &accessRecorder{}
	a := accessChain(t, h, rec, http1.LogSlowerThan(time.Millisecond))
	io := newFakeIO(0)

	oneBatch(t, a, io, 1, "/quick")
	if len(rec.recs) != 0 {
		t.Fatalf("a 100 µs answer was logged: %+v", rec.recs)
	}

	oneBatch(t, a, io, 2, "/defer")
	if len(rec.recs) != 1 {
		t.Fatalf("%d records for the deferred answer, want the one completion", len(rec.recs))
	}
	r := rec.recs[0]
	if r.Status != 201 || r.Stage != corenet.HTTP1AccessCompleted || r.BodyLen != len("later") {
		t.Errorf("completion: %+v", r)
	}
	if r.Elapsed.Duration() < 2*time.Millisecond {
		t.Errorf("Elapsed %v, want at least 2ms", r.Elapsed.Duration())
	}
}

// TestLog_elapsedIsFromTheBatchStart holds that a record's Elapsed starts at
// the batch's start: the second of two requests of a batch waited for the first.
func TestLog_elapsedIsFromTheBatchStart(t *testing.T) {
	h := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0)), took: time.Millisecond}
	rec := &accessRecorder{}
	a := accessChain(t, h, rec)
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	raw := []byte("GET /a HTTP/1.1\r\nHost: h\r\n\r\nGET /b HTTP/1.1\r\nHost: h\r\n\r\n")
	if n := a.OnData(io, ref, raw); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(io)
	if len(rec.recs) != 2 || rec.recs[0].Elapsed.Duration() != time.Millisecond || rec.recs[1].Elapsed.Duration() != 2*time.Millisecond {
		t.Fatalf("records %+v", rec.recs)
	}
	//: the next batch starts over.
	oneBatch(t, a, io, 2, "/c")
	if got := rec.recs[2].Elapsed.Duration(); got != time.Millisecond {
		t.Fatalf("next batch: %v", got)
	}
}

// TestLog_deferredHasTwoRecords holds the record pair of a deferred answer:
// same ID, same RequestID; the first without a status, the second with it.
func TestLog_deferredHasTwoRecords(t *testing.T) {
	h := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0)), late: 3 * time.Millisecond}
	rec := &accessRecorder{}
	logLink, err := http1.Log(rec, http1.LogClock(h.clk))
	if err != nil {
		t.Fatal(err)
	}
	a := http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](h, logLink, http1.RequestID()), http1.Parser{})
	io := newFakeIO(0)
	oneBatch(t, a, io, 1, "/defer")
	if len(rec.recs) != 2 {
		t.Fatalf("%d records", len(rec.recs))
	}
	first, second := rec.recs[0], rec.recs[1]
	if first.Stage != corenet.HTTP1AccessDeferred || first.Status != 0 {
		t.Errorf("first: %+v", first)
	}
	if second.Stage != corenet.HTTP1AccessCompleted || second.Status != 201 || second.Elapsed.Duration() != 3*time.Millisecond {
		t.Errorf("second: %+v", second)
	}
	if first.ID == 0 || first.ID != second.ID {
		t.Errorf("IDs %d and %d", first.ID, second.ID)
	}
	if len(first.RequestID.Bytes()) == 0 || string(first.RequestID.Bytes()) != string(second.RequestID.Bytes()) {
		t.Errorf("request ids %q and %q", first.RequestID.Bytes(), second.RequestID.Bytes())
	}
	if !stringsHasPrefixCopy(string(first.Path), "/defer") || string(second.Path) != "/defer" {
		t.Errorf("paths %q and %q", first.Path, second.Path)
	}
}

func stringsHasPrefixCopy(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// TestLog_idsDiffer holds that two lanes never produce the same ID.
func TestLog_idsDiffer(t *testing.T) {
	h := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0))}
	rec := &accessRecorder{}
	a := accessChain(t, h, rec)
	for lane := range 2 {
		io := newFakeIO(lane)
		for i := range 200 {
			oneBatch(t, a, io, uint32(i+1), "/x")
		}
	}
	seen := map[uint64]bool{}
	for _, r := range rec.recs {
		if r.ID == 0 || seen[r.ID] {
			t.Fatalf("ID %d zero or repeated", r.ID)
		}
		seen[r.ID] = true
	}
	if rec.recs[0].ID>>48 != 0 || rec.recs[200].ID>>48 != 1 {
		t.Fatalf("lanes in the IDs: %x, %x", rec.recs[0].ID, rec.recs[200].ID)
	}
}

// TestLog_sampleOneInN holds the deterministic sample: exactly one in n, and a
// slow record always, whatever its number.
func TestLog_sampleOneInN(t *testing.T) {
	h := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0))}
	rec := &accessRecorder{}
	a := accessChain(t, h, rec, http1.LogSample(100))
	io := newFakeIO(0)
	for i := range 1000 {
		oneBatch(t, a, io, uint32(i+1), "/x")
	}
	if len(rec.recs) != 10 {
		t.Fatalf("%d records, want 10", len(rec.recs))
	}
	//: with a threshold too: the slow always, the rest one in n.
	h2 := &accessH{clk: clock.NewManualClock(time.Unix(1000, 0)), took: 2 * time.Millisecond}
	rec2 := &accessRecorder{}
	a2 := accessChain(t, h2, rec2, http1.LogSample(100), http1.LogSlowerThan(time.Millisecond))
	for i := range 50 {
		oneBatch(t, a2, io, uint32(i+1), "/x")
	}
	if len(rec2.recs) != 50 {
		t.Fatalf("%d slow records, want all 50", len(rec2.recs))
	}
}

// TestLog_optionsRefused holds each option's refusal at construction.
func TestLog_optionsRefused(t *testing.T) {
	rec := &accessRecorder{}
	for _, c := range []struct {
		name string
		opt  http1.LogOption
		want string
	}{
		{"slower zero", http1.LogSlowerThan(0), "slower"},
		{"slower negative", http1.LogSlowerThan(-time.Second), "slower"},
		{"sample zero", http1.LogSample(0), "sample"},
		{"nil clock", http1.LogClock(nil), "clock"},
	} {
		if _, err := http1.Log(rec, c.opt); !misconfiguredAs(err, c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
