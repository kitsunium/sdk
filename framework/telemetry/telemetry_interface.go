// Package telemetry — the interfaces: what a producer emits to, and what the
// exporter needs of its listener and of a client.
package telemetry

import (
	"time"

	"github.com/kitsunium/sdk/pkg/v1/ipc"
)

// Emitter is where a product reports: the telemetry port. Emit copies e and
// never keeps it; it must not block and must not allocate.
type Emitter interface {
	Emit(e *Event)
}

// writer is what a greeting or a batch needs of a client.
type writer interface {
	SetWriteDeadline(t time.Time) error
	Write(b []byte) (n int, err error)
}

// accepter is what the accept loop needs of a listener.
type accepter interface {
	Accept() (*ipc.Conn, error)
}
