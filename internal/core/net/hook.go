// Package net — the call observation hook.
package net

// CallHook receives one CallValue per completed outbound call. It is a function
// port rather than an interface because it is a single-method contract with no
// state, and a func keeps the domain free of a dependency on the logger or the
// metrics package: the consumer wires it to whichever it uses.
//
// A CallHook MUST be safe for concurrent use and MUST NOT block — it runs on the
// calling goroutine, so a slow hook slows every request.
type CallHook func(call CallValue)
