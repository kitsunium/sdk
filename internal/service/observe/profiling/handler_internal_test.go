package profiling

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// serve runs one request against h and returns the recorder.
func serve(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// TestHandlerServesThePprofWire pins #1023 through httptest: heap answers a
// profile Parse reads; profile?seconds=1 answers one when the ManualClock
// advances a second, with no sleep; a second profile meanwhile is 409; the
// window bounds are 400; goroutine?debug=2 is a dump ParseGoroutines reads;
// cmdline and symbol are 404; and nothing is registered on DefaultServeMux.
// Not parallel: the CPU profiler is the process's one.
func TestHandlerServesThePprofWire(t *testing.T) {
	traceMu.Lock()
	defer traceMu.Unlock()
	clk := clock.NewManualClock(time.Unix(0, 0))
	h := NewHandler(HandlerConfig{Clock: clk})
	heap := serve(h, "/debug/pprof/heap")
	if _, err := Parse(heap.Body.Bytes()); heap.Code != http.StatusOK || err != nil ||
		heap.Header().Get(headerNoSniff) != noSniff || heap.Header().Get(headerCacheControl) != noStore {
		t.Fatalf("heap = %d, %v, headers %v", heap.Code, err, heap.Header())
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serve(h, "/debug/pprof/profile?seconds=1") }()
	clk.BlockUntil(1)
	//: the profiler is held by the first request.
	if busy := serve(h, "/debug/pprof/profile?seconds=1"); busy.Code != http.StatusConflict {
		t.Fatalf("a second profile = %d %q, want 409", busy.Code, busy.Body.String())
	}
	clk.Advance(time.Second)
	profile := <-done
	if _, err := Parse(profile.Body.Bytes()); profile.Code != http.StatusOK || err != nil {
		t.Fatalf("profile = %d, %v", profile.Code, err)
	}
	for _, bad := range []string{"0", "301", "x"} {
		if rec := serve(h, "/debug/pprof/profile?seconds="+bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("profile?seconds=%s = %d, want 400", bad, rec.Code)
		}
	}
	dump := serve(h, "/debug/pprof/goroutine?debug=2")
	if dump.Code != http.StatusOK || len(ParseGoroutines(dump.Body.Bytes())) == 0 {
		t.Fatalf("goroutine?debug=2 = %d, %d goroutines read", dump.Code, len(ParseGoroutines(dump.Body.Bytes())))
	}
	for _, hidden := range []string{"cmdline", "symbol"} {
		if rec := serve(h, "/debug/pprof/"+hidden); rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", hidden, rec.Code)
		}
	}
	if index := serve(h, "/debug/pprof/"); index.Code != http.StatusOK || index.Body.Len() == 0 {
		t.Fatalf("index = %d", index.Code)
	}
	_, pattern := http.DefaultServeMux.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/debug/pprof/"}})
	if pattern != "" {
		t.Fatalf("DefaultServeMux routes /debug/pprof/ to %q", pattern)
	}
}
