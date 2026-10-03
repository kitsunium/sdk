// Package queue — range 0.2.23.* (ADR 0054 core/data/queue block).
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
