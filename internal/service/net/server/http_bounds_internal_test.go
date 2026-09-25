package server

import (
	stdnet "net"
	"net/http"
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
	if srv.ReadHeaderTimeout != 2*time.Second || srv.ReadTimeout != 20*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, ReadTimeout = %v; want 2s and 20s", srv.ReadHeaderTimeout, srv.ReadTimeout)
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
