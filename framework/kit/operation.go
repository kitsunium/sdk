package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Operation takes a typed request and answers a typed response: an
// endpoint, a port — and, with ADR 0005, a command or a query. It is what
// [Fallback] and [Bind] give a port, and what [Replace] replaces in a test;
// a request or a response of other types does not compile. Its methods are
// unexported: only kit implements it.
//
// Every operation is a node of the graph, and runs in a span of its own
// through its whole pipeline — authentication, validation, policies, the
// Studio's mocks — whoever calls it.
type Operation[Req, Resp any] = ikit.Operation[Req, Resp]
