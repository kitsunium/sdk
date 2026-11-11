package http1_test

import (
	"bufio"
	"bytes"
	"net/http"
	"regexp"
	"strings"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// mintedID is what the link mints: lane<<48|counter, sixteen lowercase hex digits.
var mintedID = regexp.MustCompile(`^[0-9a-f]{16}$`)

// idChain is a RequestID link under a Log link, on the adapter, and a handler
// that answers by target; the records are the logger's.
func idChain(t *testing.T, opts ...http1.RequestIDOption) (*http1.LoopHandler, *accessRecorder) {
	t.Helper()
	rec := &accessRecorder{}
	logLink, err := http1.Log(rec)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		res.Body = []byte("ok")
	})
	return http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, logLink, http1.RequestID(opts...)), http1.Parser{}), rec
}

// idOf sends one request on a new connection of lane and returns the answer's
// X-Request-Id, its status and how many of that field the head carried.
func idOf(t *testing.T, a *http1.LoopHandler, lane int, slot uint32, raw string) (string, int, int) {
	t.Helper()
	io := newFakeIO(lane)
	ref := corenet.LoopConnRef{Slot: slot, Gen: 1}
	a.OnOpen(io, ref)
	if n := a.OnData(io, ref, []byte(raw)); n != len(raw) {
		t.Fatalf("%q: %d bytes taken", raw, n)
	}
	a.OnBatchEnd(io)
	res, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(io.out[ref])), nil)
	if err != nil {
		t.Fatalf("%q answered %q: %v", raw, io.out[ref], err)
	}
	defer res.Body.Close()
	return res.Header.Get("X-Request-Id"), res.StatusCode, len(res.Header.Values("X-Request-Id"))
}

func withID(id string) string {
	return "GET / HTTP/1.1\r\nHost: h\r\nX-Request-Id: " + id + "\r\n\r\n"
}

func TestRequestID_echoedAndInTheRecord(t *testing.T) {
	a, rec := idChain(t)
	got, status, n := idOf(t, a, 0, 1, withID("abc-123"))
	if got != "abc-123" || status != 200 || n != 1 {
		t.Fatalf("X-Request-Id %q, status %d, %d fields", got, status, n)
	}
	if id := rec.recs[0].RequestID; string(id.Bytes()) != "abc-123" {
		t.Fatalf("the record carries %q", id.Bytes())
	}
}

func TestRequestID_mintedDistinct(t *testing.T) {
	a, _ := idChain(t)
	seen := make(map[string]bool, 2000)
	for lane := range 2 {
		for i := range 1000 {
			got, status, _ := idOf(t, a, lane, uint32(i+1), "GET / HTTP/1.1\r\nHost: h\r\n\r\n")
			if status != 200 || !mintedID.MatchString(got) {
				t.Fatalf("lane %d request %d: %q, status %d", lane, i, got, status)
			}
			if seen[got] {
				t.Fatalf("lane %d request %d: %q minted twice", lane, i, got)
			}
			seen[got] = true
		}
	}
}

func TestRequestID_hostileIsReplacedAndServed(t *testing.T) {
	a, _ := idChain(t)
	for _, bad := range []string{
		strings.Repeat("a", 65),
		"has space",
		"caf\xc3\xa9",
		"tab\tinside",
	} {
		got, status, n := idOf(t, a, 0, 1, withID(bad))
		if status != 200 || !mintedID.MatchString(got) || n != 1 {
			t.Errorf("%q: answered %q, status %d, %d fields", bad, got, status, n)
		}
	}
	//: DEL and the other controls never reach the link: the codec refuses the
	//: whole request (RFC 9110 field-vchar, ADR 0186 §D6).
	if _, status, _ := idOf(t, a, 0, 1, withID("del\x7f")); status != 400 {
		t.Errorf("DEL: status %d, want the codec's 400", status)
	}
	//: the longest and the widest accepted.
	for _, good := range []string{strings.Repeat("a", 64), "!~"} {
		if got, status, _ := idOf(t, a, 0, 1, withID(good)); got != good || status != 200 {
			t.Errorf("%q: answered %q, status %d", good, got, status)
		}
	}
}

func TestRequestID_mintReplacesAValidOne(t *testing.T) {
	a, rec := idChain(t, http1.RequestIDMint())
	got, _, _ := idOf(t, a, 0, 1, withID("abc-123"))
	if !mintedID.MatchString(got) || got == "abc-123" {
		t.Fatalf("minted %q", got)
	}
	if string(rec.recs[0].RequestID.Bytes()) != got {
		t.Fatalf("the record carries %q, the answer %q", rec.recs[0].RequestID.Bytes(), got)
	}
}

// TestRequestID_parseResets holds that a request never inherits the id of the
// one framed before it into the same reused value.
func TestRequestID_parseResets(t *testing.T) {
	var req corenet.HTTP1RequestValue
	req.RequestID.Set([]byte("stale"))
	in := []byte("GET / HTTP/1.1\r\nHost: h\r\n\r\n")
	if n, r := (http1.Parser{}).Parse(in, &req); n != len(in) || r != http1.RefusalNone {
		t.Fatal(n, r)
	}
	if len(req.RequestID.Bytes()) != 0 {
		t.Fatalf("after Parse: %q", req.RequestID.Bytes())
	}
}

// TestRequestIDValue_refusesWhatTheWireCannotCarry holds the value's own gate:
// the codec writes it into the head, so a byte a handler sets is refused here.
func TestRequestIDValue_refusesWhatTheWireCannotCarry(t *testing.T) {
	var v corenet.HTTP1RequestIDValue
	if !v.Set([]byte("a-1")) {
		t.Fatal("refused a valid id")
	}
	for _, bad := range [][]byte{nil, {}, []byte("a\r\nX: y"), []byte("a b"), bytes.Repeat([]byte("a"), corenet.HTTP1MaxRequestID+1)} {
		if v.Set(bad) {
			t.Errorf("%q accepted", bad)
		}
		if string(v.Bytes()) != "a-1" {
			t.Errorf("%q changed the value to %q", bad, v.Bytes())
		}
	}
	v.Reset()
	if len(v.Bytes()) != 0 {
		t.Fatalf("after Reset: %q", v.Bytes())
	}
}

func TestAppendResponse_requestID(t *testing.T) {
	req := &corenet.HTTP1RequestValue{Method: []byte("GET")}
	res := &corenet.HTTP1ResponseValue{Header: []byte("A: b\r\n"), Body: []byte("x")}
	res.RequestID.Set([]byte("id-9"))
	got, err := http1.AppendResponse(nil, req, res, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "HTTP/1.1 200 OK\r\nA: b\r\nX-Request-Id: id-9\r\nContent-Length: 1\r\n\r\nx"; string(got) != want {
		t.Fatalf("%q", got)
	}
}

// TestRequestID_internalErrorCarriesIt holds that the answer the adapter
// writes for a response the writer refused can still be joined to its record.
func TestRequestID_internalErrorCarriesIt(t *testing.T) {
	rec := &accessRecorder{}
	logLink, err := http1.Log(rec)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) { res.Status = 100 })
	a := http1.HandleLoop(corenet.Chain[corenet.HTTP1Handler](app, logLink, http1.RequestID()), http1.Parser{})
	got, status, _ := idOf(t, a, 0, 1, withID("abc-123"))
	if got != "abc-123" || status != 500 {
		t.Fatalf("X-Request-Id %q, status %d", got, status)
	}
}

// pendingIDH hands every request to the worker.
type pendingIDH struct{}

func (pendingIDH) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Pending = true
}

func (pendingIDH) WorkHTTP1(_ int, _ uint64, res *corenet.HTTP1ResponseValue) {
	res.Body = []byte("worked")
}

// TestRequestID_pendingAnswerCarriesIt holds that an answer filled by the
// worker, from a response value of its own, still names the request.
func TestRequestID_pendingAnswerCarriesIt(t *testing.T) {
	h := corenet.Chain[corenet.HTTP1Handler](pendingIDH{}, http1.RequestID())
	a, out := startPend(pendingWorkerOf{HTTP1Handler: h}, 1<<16)
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	raw := withID("abc-123")
	if n := a.OnData(io, ref, []byte(raw)); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(io)
	finish(a, io, out)
	a.OnBatchEnd(io)
	if !strings.Contains(string(io.out[ref]), "X-Request-Id: abc-123\r\n") {
		t.Fatalf("%q", io.out[ref])
	}
}

// pendingWorkerOf gives a chain, which forwards no worker, its handler's.
type pendingWorkerOf struct {
	corenet.HTTP1Handler
}

func (pendingWorkerOf) WorkHTTP1(lane int, job uint64, res *corenet.HTTP1ResponseValue) {
	pendingIDH{}.WorkHTTP1(lane, job, res)
}
