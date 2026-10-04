// Package queue — range 0.2.23.* (ADR 0054 core/data/queue block), and the
// brokers' and the consumer engine's 0.3.53.* (ADR 0054 service/data/queue
// block, declared here since ADR 0160).
//
// Package queue — the two ADR 0039 siblings that act on the dead-letter store:
// sending a message there at once, and deciding what becomes of one there.
package queue

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.23.0 - 0.2.23.255

// CodeQueueMisconfigured identifies a PolicyValue field a broker cannot
// honour, refused at construction rather than at first use.
const CodeQueueMisconfigured errs.Code = 0x00_02_17_01 // 0.2.23.1

// CodeMessageTooLarge identifies a Publish whose payload exceeds
// PolicyValue.MaxMessageBytes.
const CodeMessageTooLarge errs.Code = 0x00_02_17_02 // 0.2.23.2

// CodeUnknownReceipt identifies an Ack, Nack or Extend naming a lease this
// broker never issued, or a receipt it cannot parse.
const CodeUnknownReceipt errs.Code = 0x00_02_17_03 // 0.2.23.3

// CodeLeaseExpired identifies an Ack, Nack or Extend on a lease that has
// lapsed. The call changed nothing and the message is elsewhere.
const CodeLeaseExpired errs.Code = 0x00_02_17_04 // 0.2.23.4

// CodeInvalidBatchSize identifies a Receive or DeadLetters asked for a
// non-positive number of messages.
const CodeInvalidBatchSize errs.Code = 0x00_02_17_05 // 0.2.23.5

// CodeNotRetryable identifies a handler's failure no retry can fix. DoNotRetry
// adds it to the wrap trail of the handler's own cause, so errs.HasCode finds
// it while the cause stays the origin a dead letter records (ADR 0151).
const CodeNotRetryable errs.Code = 0x00_02_17_06 // 0.2.23.6

// CodeDeadLetterNotFound identifies a replay or a deletion naming a dead
// letter the store does not hold.
const CodeDeadLetterNotFound errs.Code = 0x00_02_17_07 // 0.2.23.7

// range: 0.3.53.0 - 0.3.53.255 — the brokers' and the consumer engine's own
// codes, allocated by the service layer (LL = 3) and declared here since ADR
// 0160: a code keeps the value its layer allocated when its declaration moves.

// CodeQueueBackendFailed identifies an operation a durable broker depends on
// that its storage refused: a filesystem call the operating system refused,
// or a statement the SQL broker's database did not complete.
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

// CodeSQLQueueMisconfigured identifies an SQLConfig the SQL broker refuses at
// construction: no transactor, one it cannot join a transaction of, a dialect
// it cannot spell, or a table name it cannot interpolate safely (ADR 0151).
const CodeSQLQueueMisconfigured errs.Code = 0x00_03_35_05 // 0.3.53.5
