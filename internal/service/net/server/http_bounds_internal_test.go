package server

import (
	"io"
	stdnet "net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_httpBounds pins the resolution of the two HTTP-only options: a set
// value wins, and an unset or non-positive one keeps what the adapter did
// before the options existed.
func Test_httpBounds(t *testing.T) {
	t.Parallel()
	const read time.Duration = 30 * time.Second
	type tc struct {
		name        string
		bounds      httpBounds
		wantTimeout time.Duration
		wantBytes   int
	}
	tests := []tc{
		{"unset keeps the read budget and net/http's default", httpBounds{}, read, 0},
		{"set values win", httpBounds{readHeaderTimeout: time.Second, maxHeaderBytes: 8 << 10}, time.Second, 8 << 10},
		{"non-positive values are unset", httpBounds{readHeaderTimeout: -time.Second, maxHeaderBytes: -1}, read, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.bounds.headerTimeout(read); got != c.wantTimeout {
			t.Errorf("%s: headerTimeout = %v, want %v", c.name, got, c.wantTimeout)
		}
		if got := c.bounds.headerBytes(); got != c.wantBytes {
			t.Errorf("%s: headerBytes = %d, want %d", c.name, got, c.wantBytes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_httpAdapter_launchAppliesTheBounds pins that the options reach the
// http.Server the adapter builds, through the same Start-time hand-off the
// timeouts take (StreamGroup.resolved).
func Test_httpAdapter_launchAppliesTheBounds(t *testing.T) {
	t.Parallel()
	group := &StreamGroup{}
	for _, option := range []GroupOption{ReadTimeout(20 * time.Second), ReadHeaderTimeout(2 * time.Second), MaxHeaderBytes(4 << 10)} {
		option(group)
	}
	group.HandleHTTP(http.NotFoundHandler())
	//: resolved is where Start hands the options to the adapter.
	if handler := group.resolved(); handler == nil {
		t.Fatal("resolved returned no handler for an HTTP group")
	}
	local, remote := stdnet.Pipe()
	defer closeQuietly(t, local)
	defer closeQuietly(t, remote)
	group.httpAdapter.launch(local)
	bridge, srv, _ := group.httpAdapter.stop()
	defer swallowErr(bridge.Close())
	defer swallowErr(srv.Close())
	//: the read budget is applied per read of the body (#454), never as
	//: http.Server.ReadTimeout, which budgets the whole request.
	if srv.ReadHeaderTimeout != 2*time.Second || srv.ReadTimeout != 0 {
		t.Errorf("ReadHeaderTimeout = %v, ReadTimeout = %v; want 2s and 0", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
	//: the version counter first (#966), then the per-read body bound.
	if vc, ok := srv.Handler.(versionCounter); !ok {
		t.Errorf("Handler = %T, want the version counter", srv.Handler)
	} else if _, ok := vc.next.(readDeadlineHandler); !ok {
		t.Errorf("Handler's next = %T, want the per-read body bound", vc.next)
	}
	//: with no idle budget, the wait between requests keeps the read budget
	//: net/http used to fall back to.
	if srv.IdleTimeout != 20*time.Second {
		t.Errorf("IdleTimeout = %v, want the read budget's 20s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 4<<10 {
		t.Errorf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 4<<10)
	}
	//: and a group that set neither keeps the read budget for the header
	//: phase and net/http's default cap.
	plain := newHTTPAdapter(http.NotFoundHandler())
	plain.timeouts = corenet.TimeoutsValue{Read: corenet.DurationValue(7 * time.Second)}
	plainLocal, plainRemote := stdnet.Pipe()
	defer closeQuietly(t, plainLocal)
	defer closeQuietly(t, plainRemote)
	plain.launch(plainLocal)
	plainBridge, plainSrv, _ := plain.stop()
	defer swallowErr(plainBridge.Close())
	defer swallowErr(plainSrv.Close())
	if plainSrv.ReadHeaderTimeout != 7*time.Second || plainSrv.MaxHeaderBytes != 0 {
		t.Errorf("unset: ReadHeaderTimeout = %v, MaxHeaderBytes = %d; want 7s and 0", plainSrv.ReadHeaderTimeout, plainSrv.MaxHeaderBytes)
	}
}

// Test_idleOrRead pins the wait between two requests: the idle budget when it
// is set, else the read budget — the fallback net/http applied on its own
// while the adapter still handed it ReadTimeout (#454).
func Test_idleOrRead(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		idle, read, want time.Duration
	}{
		{idle: 5 * time.Second, read: 30 * time.Second, want: 5 * time.Second},
		{idle: 0, read: 30 * time.Second, want: 30 * time.Second},
		{idle: -time.Second, read: 30 * time.Second, want: 30 * time.Second},
		{idle: 0, read: 0, want: 0},
	} {
		if got := idleOrRead(c.idle, c.read); got != c.want {
			t.Errorf("idleOrRead(%v, %v) = %v, want %v", c.idle, c.read, got, c.want)
		}
	}
}

// Test_perReadBody pins that a zero budget installs nothing: the body stays
// unbounded, as it was, and the handler is the caller's own.
func Test_perReadBody(t *testing.T) {
	t.Parallel()
	next := http.NotFoundHandler()
	if got := perReadBody(next, 0); got == nil {
		t.Fatal("perReadBody(next, 0) = nil")
	} else if _, wrapped := got.(readDeadlineHandler); wrapped {
		t.Error("a zero read budget wrapped the handler")
	}
	if _, wrapped := perReadBody(next, time.Second).(readDeadlineHandler); !wrapped {
		t.Error("a read budget did not wrap the handler")
	}
}

// TestLimitBody_bodilessRequestKeepsNoBody pins #966's allocation criterion
// at its cause: the body bound wraps a body in http.MaxBytesReader, one
// allocation a request, and a request with no body — net/http's NoBody, a
// GET's — reaches the handler unwrapped, so the adapter allocates per request
// what net/http alone does (BenchmarkHTTP_Adapter against BenchmarkHTTP_Native:
// 64 allocations each on Go 1.27.4). A body still gets its bound.
func TestLimitBody_bodilessRequestKeepsNoBody(t *testing.T) {
	t.Parallel()
	var seen io.ReadCloser
	h := limitBody(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.Body }), 16)
	bodiless := httptest.NewRequest(http.MethodGet, "/", nil)
	bodiless.Body = http.NoBody
	h.ServeHTTP(httptest.NewRecorder(), bodiless)
	if seen != http.NoBody {
		t.Fatalf("a bodiless request reached the handler with %T, want http.NoBody", seen)
	}
	withBody := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))
	withBody.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), withBody)
	if seen == nil || seen == http.NoBody {
		t.Fatal("a body reached the handler without its bound")
	}
	if _, err := io.ReadAll(io.LimitReader(seen, 1)); err != nil {
		t.Fatalf("the bounded body: %v", err)
	}
}
