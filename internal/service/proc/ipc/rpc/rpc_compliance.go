package rpc

import (
	"github.com/kitsunium/sdk/internal/core/data/codec"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
)

// A Client is the Caller port, a HandlerFunc a Handler, a Methods table a
// Router, a server call the Request its handler reads, a client stream the
// Stream its caller reads, and a connection's format a codec.Appender.
var (
	_ corerpc.Caller  = (*Client)(nil)
	_ corerpc.Handler = HandlerFunc(nil)
	_ corerpc.Router  = Methods(nil)
	_ corerpc.Request = (*serverCall)(nil)
	_ corerpc.Stream  = (*clientStream)(nil)
	_ codec.Appender  = wireFormat{}
)
