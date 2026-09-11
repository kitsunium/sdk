// Package queue — the durable broker's failure path: handing a message back,
// renewing a lease, and the record left behind for whoever investigates.
package queue

import (
	"bytes"
	"context"
	"path"
	"strconv"
	"strings"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// deadMagic opens every dead-letter record. A file whose first line is not
// this is not one of ours, and a future format change gets a new number
// rather than a silent misparse.
const deadMagic string = "ktnq/1"

// deadHeaderSep ends the header. Everything after it, byte for byte, is the
// payload — which is why the header is line-based and the payload is not
// escaped, quoted or encoded: a queue moves arbitrary bytes, and any encoding
// applied here would be a second thing to get wrong.
const deadHeaderSep string = "\n\n"

// publicMaxRunes bounds what is copied from a failure's Public half. CLAUDE.md
// rule 4 already caps a DEFINED sentinel at 120 runes with no newline, but
// errs.NewRuntime accepts a runtime string, so a cause reaching this package
// from a consumer's own code can carry anything at all. It is bounded and
// stripped of line breaks HERE rather than trusted, because a newline in this
// field would end the header early and turn the rest of the reason into
// payload.
const publicMaxRunes int = 120

// Nack hands the message back and reports what the broker decided.
func (b *fileBroker) Nack(
	ctx context.Context, receipt corequeue.ReceiptValue, cause error,
) (verdict corequeue.NackValue, err error) {
	held := string(receipt)
	name, resolveErr := b.resolve(ctx, held)
	//: an unknown receipt and a lapsed one are different bugs.
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return corequeue.NackValue{}, resolveErr
	}
	now := b.clk.Now()
	//: the BROKER owns this decision, because the consumer is the thing that
	//: dies and a count it held would be lost by exactly that event.
	if name.Deliveries >= b.policy.MaxDeliveries {
		//: abandoned, with its cause.
		return b.nackDead(name, held, cause, now.UnixNano())
	}
	requeued := nameValue{
		At: now.Add(b.policy.RetryDelay).UnixNano(), EnqueuedAt: name.EnqueuedAt,
		Entropy: name.Entropy, Deliveries: name.Deliveries,
	}
	renameErr := b.root.Rename(
		path.Join(dirInflight, held), path.Join(dirReady, requeued.readyName()))
	//: a lease that lapsed between resolve and here is the ordinary race.
	if renameErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return corequeue.NackValue{}, b.classifyMissing("rename", held, renameErr)
	}
	//: queued again, eligible at VisibleAt.
	return corequeue.NackValue{
		Deliveries: name.Deliveries, VisibleAt: time.Unix(0, requeued.At),
	}, nil
}

// nackDead buries a message that has run out of attempts.
func (b *fileBroker) nackDead(
	name nameValue, receipt string, cause error, now int64,
) (verdict corequeue.NackValue, err error) {
	//: the cause is reduced to what a stranger investigating a queue may see.
	if buryErr := b.bury(name, receipt, describeCause(cause), now); buryErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return corequeue.NackValue{}, buryErr
	}
	//: never delivered again.
	return corequeue.NackValue{Deliveries: name.Deliveries, DeadLettered: true}, nil
}

// Extend renews a lease and mints the receipt that replaces it.
//
// A by whose deadline a name cannot carry — one that reaches past
// 2262-04-11 — is refused with QueueMisconfigured, before anything is renamed,
// so the lease the caller already holds is untouched. Validate bounds the
// policy's own durations with corequeue.MaxDeadlineOffset; this one is chosen
// at runtime and never passes through it, so the instant itself is checked.
func (b *fileBroker) Extend(
	ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration,
) (lease corequeue.LeaseValue, err error) {
	//: the two readings of a non-positive renewal are opposites.
	if by <= 0 {
		//: QueueMisconfigured, naming the argument.
		return corequeue.LeaseValue{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "Extend.by"), kerrs.Int64("value_ns", int64(by)))
	}
	deadline := b.clk.Now().Add(by)
	//: a deadline the names cannot carry would rename the message to a name
	//: nothing reads back, and hand the caller a receipt that is already
	//: unknown — the message stranded by the very call meant to keep it.
	if !nameable(deadline) {
		//: QueueMisconfigured, naming the argument.
		return corequeue.LeaseValue{}, kerrs.Wrap(corequeue.QueueMisconfigured, kerrs.WrapParams{},
			kerrs.String("field", "Extend.by"), kerrs.Int64("value_ns", int64(by)))
	}
	held := string(receipt)
	name, resolveErr := b.resolve(ctx, held)
	//: a lapsed lease is NOT renewed, even when nobody has taken the message:
	//: expiry is a deadline, not "until somebody else wants it".
	if resolveErr != nil {
		//: UnknownReceipt or LeaseExpired.
		return corequeue.LeaseValue{}, resolveErr
	}
	entropy, entropyErr := randomHex(leaseEntropyBytes)
	//: a renewal mints a new name, so it needs a new random half.
	if entropyErr != nil {
		//: QueueBackendFailed — nothing moved.
		return corequeue.LeaseValue{}, backendFailed("rand", "", entropyErr)
	}
	//: the deadline lives IN the name, so the only atomic way to change it is
	//: to replace the name — which is why Extend returns a new receipt.
	return b.renameLease(name, held, entropy, deadline.UnixNano())
}

// renameLease moves an in-flight message to a new deadline.
func (b *fileBroker) renameLease(
	name nameValue, receipt, entropy string, deadline int64,
) (lease corequeue.LeaseValue, err error) {
	renewed := nameValue{
		At: deadline, EnqueuedAt: name.EnqueuedAt, Entropy: name.Entropy,
		Deliveries: name.Deliveries, Lease: entropy,
	}
	target := renewed.inflightName()
	renameErr := b.root.Rename(path.Join(dirInflight, receipt), path.Join(dirInflight, target))
	//: a lease that lapsed between resolve and here is the ordinary race.
	if renameErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return corequeue.LeaseValue{}, b.classifyMissing("rename", receipt, renameErr)
	}
	//: a NEW receipt; the old one names nothing from here on.
	return corequeue.LeaseValue{
		Receipt: corequeue.ReceiptValue(target), ExpiresAt: renewed.AtTime(),
	}, nil
}

// bury writes the dead-letter record and then removes the in-flight message.
//
// That ORDER is the one that degrades correctly. A crash between the two
// leaves the message both abandoned and in flight; its lease lapses, it is
// buried a second time, and the second record lands on the SAME name — the
// enqueue instant, the entropy and the delivery count are all fixed by then —
// so the atomic publication overwrites it and the store holds one record. The
// opposite order would lose the message entirely, which is the one outcome
// this domain exists to prevent.
func (b *fileBroker) bury(name nameValue, receipt string, cause causeValue, now int64) error {
	payload, readErr := b.root.ReadFile(path.Join(dirInflight, receipt))
	//: the file is gone: another consumer reclaimed the lease first.
	if readErr != nil {
		//: LeaseExpired or QueueBackendFailed.
		return b.classifyMissing("read", receipt, readErr)
	}
	record := encodeDead(name, cause, now, payload)
	//: durable, because a dead letter that a crash could lose is a message
	//: that vanished with no record at all.
	if writeErr := b.publisher.WriteAtomic(
		path.Join(dirDead, name.deadName()), record, messageMode,
	); writeErr != nil {
		//: QueueBackendFailed — vfs guarantees nothing was published.
		return backendFailed("publish", dirDead, writeErr)
	}
	//: only now.
	return ignoreMissing("remove", receipt, b.root.Remove(path.Join(dirInflight, receipt)))
}

// DeadLetters returns up to max abandoned messages, oldest first, and removes
// none of them.
func (b *fileBroker) DeadLetters(
	ctx context.Context, max int,
) (dead []corequeue.DeadLetterValue, err error) {
	//: the same refusal Receive makes.
	if invalid := checkBatch(max); invalid != nil {
		//: InvalidBatchSize.
		return nil, invalid
	}
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return nil, ctx.Err()
	}
	entries, listErr := b.entriesOf(dirDead)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return nil, listErr
	}
	//: nil until something is found, so an empty store allocates nothing.
	var out []corequeue.DeadLetterValue
	//: oldest first, because fs.ReadDir sorts on a name that opens with the
	//: enqueue instant.
	for _, entry := range entries {
		record, ok, readErr := b.readDead(entry.Name())
		//: the medium refused mid-listing.
		if readErr != nil {
			//: QueueBackendFailed.
			return nil, readErr
		}
		//: not one of ours, or a record another process is still publishing.
		if !ok {
			continue
		}
		out = append(out, record)
		//: the caller asked for a bounded batch.
		if len(out) == max {
			break
		}
	}
	//: evidence, still on disk.
	return out, nil
}

// readDead reads and decodes one dead-letter record.
func (b *fileBroker) readDead(
	base string,
) (record corequeue.DeadLetterValue, ok bool, err error) {
	//: skip anything that is not a dead letter's name — notably the
	//: `.vfs-*.tmp` temporary a concurrent burial is still writing.
	if _, named := parseDead(base); !named {
		//: not ours.
		return corequeue.DeadLetterValue{}, false, nil
	}
	raw, readErr := b.root.ReadFile(path.Join(dirDead, base))
	//: a record removed between the listing and the read is not a fault.
	if readErr != nil {
		//: nil for an absent file, QueueBackendFailed otherwise.
		return corequeue.DeadLetterValue{}, false, ignoreMissing("read", base, readErr)
	}
	decoded, sound := decodeDead(raw)
	//: a record this package did not write, or one truncated by a filesystem
	//: that lost it: reported as absent rather than as a parse error, because
	//: a reader of a dead-letter store wants the records it CAN read.
	return decoded, sound, nil
}

// encodeDead renders the record: a line-based header, a blank line, and the
// payload verbatim.
//
// What it keeps of the failure is CLAUDE.md rule 4's split, applied
// deliberately: the SCREAMING_SNAKE reason, the dotted-quad code and the
// WIRE-SAFE Public half go to the store, and the Private half does not. A
// dead-letter store is read by whoever is investigating, frequently not the
// process, the host or the trust domain that produced the failure, so it is
// the wrong place for the half of an error the SDK defines as log-only. The
// message identifier is the join key back to the log line that has it.
func encodeDead(name nameValue, cause causeValue, failedAt int64, payload []byte) []byte {
	var header strings.Builder
	header.WriteString(deadMagic + "\n")
	header.WriteString("id " + name.ID() + "\n")
	header.WriteString("enqueued " + strconv.FormatInt(name.EnqueuedAt, decimalBase) + "\n")
	header.WriteString("failed " + strconv.FormatInt(failedAt, decimalBase) + "\n")
	header.WriteString("deliveries " + strconv.Itoa(name.Deliveries) + "\n")
	header.WriteString("code " + strconv.FormatUint(uint64(cause.code), decimalBase) + "\n")
	header.WriteString("reason " + oneLine(cause.reason) + "\n")
	header.WriteString("public " + oneLine(cause.public) + "\n")
	header.WriteString("\n")
	record := make([]byte, 0, header.Len()+len(payload))
	record = append(record, header.String()...)
	//: verbatim: a queue moves arbitrary bytes, and any encoding applied here
	//: would be a second thing to get wrong.
	return append(record, payload...)
}

// decodeDead reads a record back.
func decodeDead(raw []byte) (record corequeue.DeadLetterValue, ok bool) {
	head, payload, split := bytes.Cut(raw, []byte(deadHeaderSep))
	lines := strings.Split(string(head), "\n")
	//: a record with no header separator was truncated, or was never ours.
	if !split || len(lines) == 0 || lines[0] != deadMagic {
		//: unreadable.
		return corequeue.DeadLetterValue{}, false
	}
	fields := map[string]string{}
	//: every line after the magic, skipping anything without a separator.
	for _, line := range lines[1:] {
		key, value, found := strings.Cut(line, " ")
		//: a line with no separator is not a field.
		if found {
			fields[key] = value
		}
	}
	//: assembled from whatever the header actually carried.
	return corequeue.DeadLetterValue{
		Message: corequeue.MessageValue{
			ID: fields["id"], Payload: payload, EnqueuedAt: nanoField(fields["enqueued"]),
		},
		FailedAt: nanoField(fields["failed"]), Deliveries: intField(fields["deliveries"]),
		Code: uint32(intField(fields["code"])), Reason: fields["reason"], Cause: fields["public"],
	}, true
}

// oneLine makes a value safe for a line-based header: no line break, bounded
// length.
//
// A newline here would end the header early and turn the rest of the reason
// into payload, which is the classic injection into a line-oriented format.
func oneLine(value string) string {
	stripped := strings.NewReplacer("\n", " ", "\r", " ").Replace(value)
	runes := []rune(stripped)
	//: bounded, because errs.NewRuntime accepts a runtime string and a cause
	//: from a consumer's own code can carry anything at all.
	if len(runes) > publicMaxRunes {
		//: truncated rather than refused: the record is evidence, and half a
		//: reason beats no record.
		return string(runes[:publicMaxRunes])
	}
	//: usable as one header line.
	return stripped
}

// nanoField reads an instant from a header field, yielding the zero Time when
// the field is absent or unreadable.
func nanoField(value string) time.Time {
	nanos, err := strconv.ParseInt(value, decimalBase, int64Bits)
	//: a missing or corrupt field is reported as an unknown instant, not as a
	//: refusal to read the record at all.
	if err != nil {
		//: the zero Time.
		return time.Time{}
	}
	//: the instant the burial recorded.
	return time.Unix(0, nanos)
}

// intField reads a count from a header field, yielding 0 when it is absent or
// unreadable.
func intField(value string) int {
	count, err := strconv.Atoi(value)
	//: same rule as nanoField: a corrupt field costs its own value and not
	//: the whole record.
	if err != nil {
		//: unknown.
		return 0
	}
	//: the recorded count.
	return count
}
