// Package kit — what a listener needs of the socket it accepts on.
package kit

import "github.com/kitsunium/sdk/pkg/v1/proc/ipc"

// accepter is what a listener's accept loop needs of its socket.
type accepter interface {
	Accept() (*ipc.Conn, error)
}
