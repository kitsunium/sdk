package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Command changes something: a typed input, a typed result, and one handler
// — the one declared with it. Declare it with [Service].Command; dispatch it
// with [Command].Dispatch from any building block, which draws a dispatches
// edge from the caller; give it a route with [Command].Expose.
//
// A dispatch runs, in this order: the caller's user when the command asks
// for one ([Auth], implied by [Command].Allow) — an in-process dispatch
// carries its caller's and is not authenticated again —, the input's
// validate tags, the policies ([RateLimit], [RateLimitPerClient],
// [Timeout], [Bulkhead]), the key ([Command].Key), the authorization
// ([Command].Allow, then [Command].Authorize), then the handler. A queued
// command ([Queued]) is checked at its dispatch, as its caller — its
// validate tags, its authorization, its key —, and handled by its queue's
// consumer, the policies around the handling.
//
// ADR 0004's step 2 makes a command its unit of work: its authorization and
// its handler will run in one transaction, which holds its effects until
// the commit. Until then, a command's writes and effects are made as the
// handler makes them.
type Command[C, R any] = ikit.Command[C, R]

// CommandOption configures a command: [Queued]; the options every operation
// takes — [Auth], [AuthOptional], [RateLimit], [RateLimitPerClient],
// [Timeout], [Bulkhead]; and, for a queued one, [MaxDeliveries] and
// [Parallelism].
type CommandOption = ikit.CommandConfigurer

// queued is Queued's body: decl_gen.go writes Queued, from the
// design, as one call of it.
func queued() CommandOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Queued()
}

// noTransaction is NoTransaction's body: decl_gen.go writes NoTransaction, from the
// design, as one call of it.
func noTransaction() CommandOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NoTransaction()
}
