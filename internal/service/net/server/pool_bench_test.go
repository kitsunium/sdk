// Package server — the per-connection wrapper's acquire and release.
package server

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// BenchmarkAcquireRelease_ReadBuffer16K is a connection's wrapper taken and
// given back for a group whose ReadBufferSize is 16 KiB (#810): the scratch
// buffer comes from kernel/concur/buffer's 16 KiB class, so a steady accept
// loop allocates nothing for it. Before the classes, the shared pool handed
// out 1 KiB and every such acquire allocated its own 16 KiB.
func BenchmarkAcquireRelease_ReadBuffer16K(b *testing.B) {
	srv := New()
	group := &StreamGroup{name: "api", limits: corenet.LimitsValue{ReadBufferSize: 16 << 10}}
	socket := &fakeSocket{}
	//: one round first, so the pools are warm.
	srv.release(srv.acquire(socket, group))
	b.ReportAllocs()
	for b.Loop() {
		srv.release(srv.acquire(socket, group))
	}
}
