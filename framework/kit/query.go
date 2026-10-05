package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Query reads and changes nothing: a typed input, a typed result, and one
// handler — the one declared with it. Declare it with [Service].Query; ask it
// with [Query].Ask from any building block, which draws an asks edge from
// the caller; give it a route with [Query].Expose. The static analysis warns
// of a query whose code writes a store, fires a workflow, publishes, sends a
// mail or dispatches a command.
//
// A question runs, in this order: the caller's user when the query asks for
// one ([Auth], implied by [Query].Allow), the input's validate tags, the
// policies, the authorization ([Query].Allow, then [Query].Authorize), then
// the handler, on the caller's goroutine.
type Query[Q, R any] = ikit.Query[Q, R]

// QueryOption configures a query: the options every operation takes —
// [Auth], [AuthOptional], [RateLimit], [RateLimitPerClient], [Timeout],
// [Bulkhead].
type QueryOption = ikit.QueryConfigurer
