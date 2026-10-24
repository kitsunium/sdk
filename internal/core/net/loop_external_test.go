// Package net_test — the loop engine's contract.
package net_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Test_OptionKind_String pins the name of every option kind and the table's
// length against the constant block. A refusal names the option it refused
// with this string, so a kind that rendered "unknown" — a constant added
// without its name — would refuse an option without saying which.
func Test_OptionKind_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		kind corenet.OptionKind
		want string
	}
	tests := []tc{
		{"the zero kind", corenet.OptionNone, "none"},
		{"TLS", corenet.OptionTLS, "TLS"},
		{"middleware", corenet.OptionMiddleware, "Use"},
		{"read timeout", corenet.OptionReadTimeout, "ReadTimeout"},
		{"write timeout", corenet.OptionWriteTimeout, "WriteTimeout"},
		{"idle timeout", corenet.OptionIdleTimeout, "IdleTimeout"},
		{"read header timeout", corenet.OptionReadHeaderTimeout, "ReadHeaderTimeout"},
		{"max header bytes", corenet.OptionMaxHeaderBytes, "MaxHeaderBytes"},
		{"handshake timeout", corenet.OptionHandshakeTimeout, "HandshakeTimeout"},
		{"read buffer size", corenet.OptionReadBufferSize, "ReadBufferSize"},
		{"max packet size", corenet.OptionMaxPacketSize, "MaxPacketSize"},
		{"batch size", corenet.OptionBatchSize, "BatchSize"},
		{"shards", corenet.OptionShards, "Shards"},
		{"max conns", corenet.OptionMaxConns, "MaxConns"},
		{"adopt", corenet.OptionAdopt, "Adopt"},
		{"pre-handshake", corenet.OptionPreHandshake, "PreHandshake"},
		//: past the block, the value is reported, never indexed out of range.
		{"one past the last kind", corenet.OptionPreHandshake + 1, "unknown"},
		{"the largest value", corenet.OptionKind(255), "unknown"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.kind.String(); got != c.want {
			t.Errorf("OptionKind(%d).String() = %q, want %q", uint8(c.kind), got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_LoopConnRef_zero pins that the zero reference names no connection: a
// live connection's generation is never zero, so a handler holding a zero
// LoopConnRef — a map miss, a field never set — holds a stale reference on
// every loop rather than the first connection of ring 0.
func Test_LoopConnRef_zero(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ref  corenet.LoopConnRef
		zero bool
	}
	tests := []tc{
		{"the zero value", corenet.LoopConnRef{}, true},
		{"slot 0 of ring 0, generation 1", corenet.LoopConnRef{Gen: 1}, false},
		{"another ring", corenet.LoopConnRef{Ring: 1, Gen: 1}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: comparable, so a handler can key a map with it.
		seen := map[corenet.LoopConnRef]bool{c.ref: true}
		if got := seen[corenet.LoopConnRef{}]; got != c.zero {
			t.Errorf("%+v == zero: %v, want %v", c.ref, got, c.zero)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_LoopBackend_String pins the spelling of every backend, which State and
// the engine's start line print, and that the zero value is auto.
func Test_LoopBackend_String(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		backend corenet.LoopBackend
		want    string
	}
	tests := []tc{
		{"the zero value", corenet.LoopBackend(0), "auto"},
		{"io_uring", corenet.LoopBackendIOURing, "iouring"},
		{"epoll", corenet.LoopBackendEpoll, "epoll"},
		{"portable", corenet.LoopBackendPortable, "portable"},
		{"one past the last backend", corenet.LoopBackendPortable + 1, "unknown"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.backend.String(); got != c.want {
			t.Errorf("LoopBackend(%d).String() = %q, want %q", uint8(c.backend), got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
