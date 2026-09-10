// Package queue — range 0.3.53.* (ADR 0054 service/queue block).
package queue

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.53.0 - 0.3.53.255

// CodeQueueBackendFailed identifies a filesystem operation the durable broker
// depends on that the operating system refused.
const CodeQueueBackendFailed errs.Code = 0x00_03_35_01 // 0.3.53.1

// CodeQueueDirectoryUnusable identifies a queue directory that cannot be
// prepared, is not a directory, or is writable by accounts that must not be
// able to inject or remove messages.
const CodeQueueDirectoryUnusable errs.Code = 0x00_03_35_02 // 0.3.53.2

// CodeConsumerMisconfigured identifies a ConsumerConfig the engine refuses:
// no Handler, or a Handler whose author has not asserted that it is
// idempotent under at-least-once delivery.
const CodeConsumerMisconfigured errs.Code = 0x00_03_35_03 // 0.3.53.3

// CodeHandlerPanicked identifies a handler whose call panicked; the engine
// recovered it, nacked the message, and kept consuming.
//
// There is deliberately no companion CodeHandlerFailed. A handler that merely
// returns an error hands that error to Nack UNMODIFIED — see Consume's guard
// — because the destination is a dead-letter record whose structured fields
// already say "a handler failed", and an engine verdict wrapped around the
// cause would displace the reason an investigator needs.
const CodeHandlerPanicked errs.Code = 0x00_03_35_04 // 0.3.53.4
