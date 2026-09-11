// Package nettransport — netSink, the terminal per-record network sink. It ships
// each formatted record straight to the transport seam under a mutex, mirroring
// the syslog sink: the send is synchronous, so the recycled payload p is fully
// consumed before Write returns and needs no defensive clone (the Write path is
// 0 alloc — see BENCH.md). Non-blocking back-pressure is provided by the async
// middleware composed around it, not here.
package nettransport

import (
	"context"
	"sync"

	corelogger "github.com/kitsunium/sdk/internal/core/logger"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// sendFunc ships one formatted record over the transport, returning a non-nil
// error to fail the write. A func type (not an interface) keeps the concrete
// transport (a dialed net.Conn or an http.Client closure) captured in client.go
// and lets tests inject a recording closure with no real network.
type sendFunc func(ctx context.Context, p []byte) error

// netSink is the unexported per-record sink behind core/logger.Sink. It owns the
// transport's send + close seams and serialises both under mu so a Close never
// races an in-flight send.
type netSink struct {
	// network is the protocol identifier (tcp/udp/http) used only for error
	// fields — never the address (SSRF/secret gate).
	network string
	// send ships one record over the transport; captured at construction.
	send sendFunc
	// closer releases the transport: net.Conn.Close for tcp/udp; for http, the
	// idle connections of the pool the sink owns — the default client's — and
	// nothing of a caller-supplied client's.
	closer func() error
	// mu serialises send against Close so the transport is never used after or
	// during its own close.
	mu sync.Mutex
}

// newNetSink builds a per-record sink over the send/close seams. A nil closer is
// substituted with a no-op so Close stays branch-free.
func newNetSink(network string, send sendFunc, closer func() error) *netSink {
	//: degrade a nil closer to a no-op so Close never dereferences nil.
	if closer == nil {
		//: a seam with nothing of its own to release — a no-op is correct.
		closer = func() error { return nil }
	}
	//: hand back the constructed sink owning the seams.
	return &netSink{network: network, send: send, closer: closer}
}

// Write ships p over the transport synchronously under the mutex. The payload is
// consumed before Write returns, so no clone is needed even though the caller
// recycles p (0 alloc). A cancelled context skips the send.
func (s *netSink) Write(ctx context.Context, r corelogger.RecordEvent, p []byte) (n int, err error) {
	//: honour cancellation so a doomed record does not waste a packet.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation under the write sentinel (typed-errors rule).
		return 0, wrapWrite(ctx.Err(), s.network, len(p))
	}
	//: serialise the send against Close via the mutex.
	s.mu.Lock()
	serr := s.send(ctx, p)
	s.mu.Unlock()
	//: wrap any transport error with the write sentinel semantics.
	if serr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return 0, wrapWrite(serr, s.network, len(p))
	}
	//: happy path — the whole payload was accepted.
	return len(p), nil
}

// Flush is a no-op: each record is shipped synchronously by Write, so nothing is
// buffered on this side (the async ring upstream owns buffering). It still
// honours cancellation to satisfy the Sink contract.
func (s *netSink) Flush(ctx context.Context) error {
	//: honour cancellation even though there is nothing buffered to flush.
	if ctx != nil && ctx.Err() != nil {
		//: surface the cancellation under the write sentinel.
		return errs.Wrap(ctx.Err(), errs.WrapParams{
			Code:    CodeNetTransportWriteFailed,
			Reason:  "NET_TRANSPORT_WRITE_FAILED",
			Public:  "Network transport flush aborted due to cancellation",
			Private: "service/writer/nettransport.Flush saw a cancelled context",
		}, errs.String("network", s.network))
	}
	//: per-record sends settle synchronously — nothing buffered here.
	return nil
}

// Close releases the transport under the mutex. Subsequent sends fail with the
// transport's own closed error, wrapped as NetTransportWriteFailed.
func (s *netSink) Close() error {
	//: serialise the close against an in-flight send via the same mutex.
	s.mu.Lock()
	cerr := s.closer()
	s.mu.Unlock()
	//: wrap any close error under the write sentinel semantics.
	if cerr != nil {
		//: propagate through errs.Wrap so errors.Is still catches the cause.
		return wrapWrite(cerr, s.network, 0)
	}
	//: happy path — nothing to report.
	return nil
}
