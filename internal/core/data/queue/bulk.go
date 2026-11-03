package queue

import "time"

// AckOutcome is what [Acker.AckMany] did with one receipt:
// [AckAcknowledged], [AckLeaseExpired], [AckUnknownReceipt], or
// [AckNotAttempted] — the zero — when a failure stopped the call before it.
type AckOutcome uint8

// The four outcomes [Acker.AckMany] reports for one receipt. The zero is
// the one a broker never reached, so a result cut short by a failure says
// which receipts it did not try rather than reading as acknowledged.
const (
	// AckNotAttempted is the zero AckOutcome: the broker returned an error
	// before it reached this receipt, which is still held — or lapsed — as it
	// was before the call.
	AckNotAttempted AckOutcome = iota
	// AckAcknowledged is a receipt whose message was removed for good, as
	// [Broker.Ack] removes it.
	AckAcknowledged
	// AckLeaseExpired is a receipt whose lease had lapsed, or whose message
	// another call already removed or moved: nothing was removed, as
	// [Broker.Ack] answers [LeaseExpired].
	AckLeaseExpired
	// AckUnknownReceipt is a receipt this broker never issued or cannot parse:
	// nothing was removed, as [Broker.Ack] answers [UnknownReceipt].
	AckUnknownReceipt
)

// AckResultValue is what [Acker.AckMany] reports: one [AckOutcome] per
// receipt it was given, in the same order.
type AckResultValue struct {
	// Outcomes holds the outcome of each receipt, Outcomes[i] for the i-th;
	// it is as long as the batch, even beside an error.
	Outcomes []AckOutcome
}

// PurgeValue is what [Purger.Purge] removed: how many live messages were
// waiting, and how many were leased.
type PurgeValue struct {
	// Queued is how many waiting messages were removed, visible or not yet.
	Queued int
	// Leased is how many leased messages were removed — a lapsed lease
	// nobody had reclaimed yet among them.
	Leased int
}

// StatsValue is a queue's backlog as [Counter.Stats] reads it: three counts
// and two instants, and no payload.
type StatsValue struct {
	// OldestEnqueuedAt is the enqueue instant of the oldest live message —
	// waiting or leased —, the zero time.Time when there is none. Its age
	// is how long the queue has been behind.
	OldestEnqueuedAt time.Time
	// NextDueAt is the earliest instant a waiting message becomes visible —
	// an instant already past when one is visible now —, the zero
	// time.Time when nothing waits.
	NextDueAt time.Time
	// Queued counts the messages waiting for a consumer, visible now or
	// held until a later instant.
	Queued int64
	// Leased counts the messages a consumer leases, a lapsed lease nobody
	// reclaimed yet among them.
	Leased int64
	// Dead counts the dead letters.
	Dead int64
}
