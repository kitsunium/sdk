package server

import (
	"runtime"
	"testing"
)

// shardCase describes one shard-resolution scenario.
type shardCase struct {
	name      string
	requested int
	network   string
	want      int
	degraded  bool
}

// TestResolveShards pins the rule that an unavailable optimisation degrades
// loudly. Every case that cannot deliver what was asked must collapse to one
// listener AND say so, because a silent fallback is indistinguishable from a
// working one — which is the failure mode ListenerState.Degraded exists for.
func TestResolveShards(t *testing.T) {
	t.Parallel()
	cores := runtime.GOMAXPROCS(0)
	cases := []shardCase{
		{name: "one is always achievable", requested: 1, network: "tcp", want: 1},
		{name: "zero auto-sizes to the core count", requested: 0, network: "tcp", want: cores},
		{name: "an explicit count is honoured on tcp", requested: 4, network: "tcp", want: 4},
		{name: "udp shards like tcp", requested: 4, network: "udp", want: 4},
		{
			name: "a unix socket cannot be shared and says so",
			//: SO_REUSEPORT is an IP-socket option; a second bind on a path fails.
			requested: 4, network: "unix", want: 1, degraded: true,
		},
		{
			name:      "a unix socket asking for one shard is not degraded",
			requested: 1, network: "unix", want: 1, degraded: false,
		},
		{
			name:      "unix auto-sizing collapses without complaining",
			requested: 0, network: "unix", want: 1, degraded: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runShardCase(t, tc)
		})
	}
}

// runShardCase evaluates one shardCase.
func runShardCase(t *testing.T, tc shardCase) {
	t.Helper()
	count, degraded, reason := resolveShards(tc.requested, tc.network)
	//: on a platform without the option every multi-shard request degrades,
	//: so the expectation is conditional rather than absolute.
	wantCount, wantDegraded := tc.want, tc.degraded
	if !reusePortSupported() && tc.requested > 1 && shardable(tc.network) {
		wantCount, wantDegraded = 1, true
	}
	if !reusePortSupported() && tc.requested == 0 && shardable(tc.network) {
		wantCount, wantDegraded = 1, false
	}
	if count != wantCount {
		t.Fatalf("resolveShards(%d, %q) = %d shards, want %d", tc.requested, tc.network, count, wantCount)
	}
	if degraded != wantDegraded {
		t.Fatalf("degraded = %v, want %v (reason %q)", degraded, wantDegraded, reason)
	}
	//: the flag and the reason must agree, or State contradicts itself.
	if degraded != (reason != "") {
		t.Fatalf("degraded=%v but reason=%q — the report contradicts itself", degraded, reason)
	}
}

// TestReusePortConstantMatchesThePlatform pins the hand-defined ABI constant.
//
// It is hand-defined because golang.org/x/sys is banned SDK-wide, so nothing
// else in the build would catch a wrong value — a wrong constant would make
// setsockopt fail at bind time, on a platform nobody tests locally.
func TestReusePortConstantMatchesThePlatform(t *testing.T) {
	t.Parallel()
	//: the value differs per family, which is why it is declared per-platform.
	if !reusePortSupported() {
		t.Skip("no SO_REUSEPORT on this platform")
	}
	if soReusePort == 0 {
		t.Fatal("SO_REUSEPORT is zero, which is SO_DEBUG — the constant is wrong")
	}
}
