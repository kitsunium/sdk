// Package worker — Every layers a ticker loop on top of the LoopDaemon
// lifecycle. Held in its own file so worker.go stays focused on the LoopDaemon
// surface.
//
// Package worker provides LoopDaemon, a generic goroutine lifecycle primitive.
// A LoopDaemon spawns a single background goroutine running a Loop, exposes an
// idempotent Stop that signals the loop to exit and joins it, and a Done
// channel closed once the loop has returned. Stdlib-only and domain-neutral:
// any background drainer, batcher, ticker, or watch loop can reuse it.
//
// Three real consumers collapse onto this primitive — the async middleware
// drainer and the s3/cloudwatch batching sinks — each previously carried a
// byte-identical stop/stopOnce/done/doneOnce scaffold. The mechanism lives
// here; the loop body stays with the consumer (ADR 0014 §D6).
//
// The "Daemon" role suffix (the linter requires a recognized role suffix on
// every exported struct, and the bare noun is rejected); Start / Every are the
// idiomatic constructors, with NewLoopDaemon as the New-prefixed alias the
// struct-constructor lint expects.
package worker
