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

// Queued sends a command to the background. Dispatch returns once the
// command's own queue accepted it — a file queue under the data directory,
// memory without one —, and a consumer handles it, [Parallelism] at a time,
// retried after half a second up to [MaxDeliveries] attempts (5), then
// dead-lettered. It answers nothing: its result is [Empty], and exposed, it
// answers 202 Accepted.
//
// Delivery is at least once: Queued is the product's promise that the
// handler tolerates a redelivery, and a [Command].Key narrows the duplicates
// without removing them. The handler runs in the dispatcher's trace, as the
// user who dispatched it ([UserID]); what the auth handler said about that
// user is not queued. A queued command's input is a contract with the
// messages already queued: add a field, never rename one.
func Queued() CommandOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Queued()
}

// NoTransaction runs a command outside a transaction of its own: its
// writes land as its handler makes them, and its effects leave when made —
// a failed command may have written or announced something. On the data
// directory and in memory, where a writing command's transaction takes the
// writer turn, it lets such commands run side by side. Dispatched inside
// another command, it still runs in that one's transaction.
func NoTransaction() CommandOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NoTransaction()
}
