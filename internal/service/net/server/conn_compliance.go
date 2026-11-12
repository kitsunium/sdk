package server

import (
	"io"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The pooled connection wrapper must still satisfy the port it is handed to
// handlers as; a drift here is silent until a handler calls the missing method.
// A handler's io.Copy finds the socket's own copy through ReaderFrom and
// WriterTo, and the standard library's concrete assertions through Unwrap (#970).
var (
	_ corenet.Conn          = (*conn)(nil)
	_ io.ReaderFrom         = (*conn)(nil)
	_ io.WriterTo           = (*conn)(nil)
	_ corenet.ConnUnwrapper = (*conn)(nil)
	_ corenet.ConnUnwrapper = (*trackedConn)(nil)
	_ io.WriterTo           = (*trackedConn)(nil)
)
