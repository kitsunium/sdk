package server_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// TestShardedListenerReportsItsShardCount pins that sharding is actually in
// effect rather than silently collapsing to one listener. Without this, the
// SO_REUSEPORT benchmark could be comparing a sharded server against itself and
// reporting "no gain" for entirely the wrong reason.
func TestShardedListenerReportsItsShardCount(t *testing.T) {
	t.Parallel()
	srv := server.New()
	srv.Group("sharded",
		server.Listen("tcp", "127.0.0.1:0"),
		server.Shards(4),
	).HandleFunc(noopHandler)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	state := srv.State()
	//: one row per address, not one per shard.
	if len(state.Listeners) != 1 {
		t.Fatalf("listeners = %d, want 1 row for 1 address", len(state.Listeners))
	}
	listener := state.Listeners[0]
	//: on a platform without SO_REUSEPORT the honest answer is a degradation.
	if listener.Degraded {
		t.Skipf("sharding unavailable here: %s", listener.DegradedReason)
	}
	if listener.Shards != 4 {
		t.Fatalf("Shards = %d, want 4 — the request collapsed silently", listener.Shards)
	}
}

// TestShardedListenerStillServes pins that four listeners on one address serve
// traffic correctly, not merely that they bound.
func TestShardedListenerStillServes(t *testing.T) {
	t.Parallel()
	srv, addr := startEchoSharded(t, 4)
	for range 8 {
		if got := roundTrip(t, addr, "sharded"); got != "sharded\n" {
			t.Fatalf("echo = %q", got)
		}
	}
	//: every connection must have been accounted for, whichever shard took it.
	waitFor(t, func() bool { return srv.State().TotalConns == 8 })
}

// TestUnixListenerCannotShardAndSaysSo pins the honest degradation: a Unix
// socket cannot carry several listeners, and an explicit request is reported
// rather than quietly ignored.
func TestUnixListenerCannotShardAndSaysSo(t *testing.T) {
	t.Parallel()
	sock := t.TempDir() + "/sharded.sock"
	srv := server.New()
	srv.Group("unix",
		server.Listen("unix", sock),
		server.Shards(4),
	).HandleFunc(noopHandler)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	listener := srv.State().Listeners[0]
	if listener.Shards != 1 {
		t.Fatalf("Shards = %d, want 1 on a unix socket", listener.Shards)
	}
	if !listener.Degraded || listener.DegradedReason == "" {
		t.Fatal("a unix socket silently ignored an explicit shard request")
	}
	if !srv.State().Degraded() {
		t.Fatal("State.Degraded did not surface the listener's degradation")
	}
}

// TestAutoShardingIsNotADegradation pins that the default — zero, meaning "one
// per core" — is never reported as a fallback, because the caller expressed no
// expectation that could be disappointed.
func TestAutoShardingIsNotADegradation(t *testing.T) {
	t.Parallel()
	srv, _ := startEchoSharded(t, 0)
	listener := srv.State().Listeners[0]
	if listener.Degraded {
		t.Fatalf("auto-sizing reported a degradation: %s", listener.DegradedReason)
	}
	if listener.Shards < 1 {
		t.Fatalf("Shards = %d, want at least 1", listener.Shards)
	}
}

// startEchoSharded starts an echo server with the given shard count.
func startEchoSharded(t *testing.T, shards int) (srv *server.Server, addr string) {
	t.Helper()
	srv = server.New()
	srv.Group("echo",
		server.Listen("tcp", "127.0.0.1:0"),
		server.Shards(shards),
	).HandleFunc(echoHandler)
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })
	return srv, srv.State().Listeners[0].Address
}
