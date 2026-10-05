package telemetry

import (
	"time"

	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// writer is what a greeting or a batch needs of a client.
type writer interface {
	SetWriteDeadline(t time.Time) error
	Write(b []byte) (n int, err error)
}

// accepter is what the accept loop needs of a listener.
type accepter interface {
	Accept() (*ipc.Conn, error)
}
