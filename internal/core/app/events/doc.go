// Package events — ranges 0.2.22.* (ADR 0053 core/app/events block) and
// 0.3.52.* (ADR 0053 service/app/events block, declared here since ADR 0160).
//
// Package events — declares the sentinel *errs.Error outcomes of the domain:
// the registration refusals, the control sentinel, and the bus's verdicts on
// a dispatch. Each var's name equals its errs.Define Reason in SCREAMING_SNAKE
// form.
//
// Package events declares the SDK's IN-PROCESS event bus port: the [Listener]
// that reacts to one event, the [SubscriptionValue] that registers it at a
// priority, and the [Bus] that dispatches a published event to all of them.
// A core sibling admitted by ADR 0053.
//
// # events is not a queue
//
// This is the frontier the whole domain rests on, and it is stated first
// because getting it wrong produces a bus that guarantees neither half:
//
//   - events is IN-PROCESS, SYNCHRONOUS and SAME-GOROUTINE. Publish returns
//     when every listener has returned. There is no durability, no retry, no
//     dead-letter queue and no delivery beyond this process. If the process
//     dies mid-dispatch, the event never happened as far as anything outside
//     memory is concerned.
//   - A queue is INTER-PROCESS, ASYNCHRONOUS and DURABLE, with retries and a
//     dead-letter path.
//
// A caller who wants "asynchronous but reliable" is asking for a queue, and
// must be sent to one rather than served half of it here. Handing them a
// goroutine per listener would give them asynchrony without durability, which
// is the worst of both: work that can be lost and no record that it was.
//
// # What the bus is for
//
// One fact, several independent consequences, all inside the caller's
// transaction. "The order was placed" is one fact; writing the audit row,
// invalidating the cache and stamping the metric are three consequences that
// have nothing to say to each other. Without a bus the publisher imports all
// three; with one it imports none.
//
// # The port
//
// [Listener] is a FUNCTION type rather than an interface — the shape
// internal/core/CLAUDE.md already admits for resilience.Operation,
// scheduler.Job and lifecycle.Start. ADR 0039's rule is that a published port
// must not grow a method, because pkg/v1 aliases publish the shape and Go
// interfaces are structural; a func type satisfies that rule structurally,
// since it cannot grow a method at all.
//
// The engine, the priority ordering, the panic guard and the TYPED
// registration front end live in internal/service/app/events; this package owns
// the contract, the three domain values, and the typed sentinels a
// registration refuses with.
//
// Package events — hosts DispatchValue, the report of one Publish.
//
// Package events — hosts SubscriptionValue and the Priority that orders it.
package events
