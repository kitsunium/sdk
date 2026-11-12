package stream

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// DefaultMaxRecordBytes bounds one record when [PolicyValue.MaxRecordBytes]
// is left at zero: one mebibyte, the queue's DefaultMaxMessageBytes and
// Kafka's default message.max.bytes.
const DefaultMaxRecordBytes int = 1 << 20

// DefaultSegmentBytes is a segment's size when [PolicyValue.SegmentBytes] is
// left at zero: 64 MiB, kernel/fs/seglog's and the consensus log's.
const DefaultSegmentBytes int64 = 64 << 20

// PolicyValue is what an engine enforces on a stream: how many partitions,
// how large a record, how long and how much it keeps, how much it may hold
// before producers wait, and how large a segment a file engine writes.
//
// # Its zero value WORKS, and that is the decision
//
// A stream's zero policy is one partition, records of up to
// [DefaultMaxRecordBytes], kept forever, never full, never compacted, in
// segments of [DefaultSegmentBytes]. Every zero here has one reading (ADR 0031's clamp
// branch): unlike the queue's visibility timeout, no field has two opposite
// readings of zero, so none is refused at zero. What is refused is a
// negative value — it is not a bound, and the caller who wrote it meant
// something —, a [PolicyValue.BlockTimeout] with no [PolicyValue.MaxBytes]
// to wait under, and a MaxBytes below one record, under which no Append
// could ever succeed.
type PolicyValue struct {
	// Partitions is how many partitions the stream has, fixed for its life.
	// Zero means one; negative is refused.
	Partitions int
	// MaxRecordBytes bounds one record, its key and payload together. A
	// larger record is refused at [Stream.Append] with [RecordTooLarge]. Zero
	// means [DefaultMaxRecordBytes]; negative is refused.
	MaxRecordBytes int
	// RetainFor is how long a record is kept, measured from its
	// [EntryValue.AppendedAt]: older records are trimmed by the engine, a
	// file engine dropping whole segments only. Zero keeps every record for
	// good; negative is refused.
	RetainFor time.Duration
	// RetainBytes is how many bytes of records a partition keeps: above it
	// the engine trims the oldest. Zero is unbounded; negative is refused.
	RetainBytes int64
	// MaxBytes is how many bytes of records the stream may hold before
	// [Stream.Append] waits for room — the producers' backpressure, for a
	// stream whose retention is bounded by its readers rather than by time.
	// Zero is unbounded; negative is refused, and so is a positive value
	// below the normalized MaxRecordBytes, under which no record could fit.
	MaxBytes int64
	// BlockTimeout is how long an Append waits for room under MaxBytes before
	// it is refused with [StreamFull]. Zero refuses at once. Negative is
	// refused, and so is a positive value with no MaxBytes, which would wait
	// for nothing.
	BlockTimeout time.Duration
	// SegmentBytes is the size of the segments a file engine writes, and the
	// unit its retention releases. Zero means [DefaultSegmentBytes]; negative
	// is refused. Engines that keep no segments ignore it.
	SegmentBytes int64
	// CompactAfter is how old a record must be before a [Compactor] may
	// drop it, measured from its [EntryValue.AppendedAt]: a reader that
	// stays within that window behind the head sees every update, and a
	// tombstone is kept one more CompactAfter after it became compactable.
	// Zero never compacts; negative is refused.
	CompactAfter time.Duration
}

// Validate reports whether the policy is one an engine can honour, and
// refuses it BY FIELD ([StreamMisconfigured], the "field" field naming
// which) when it is not.
//
// It lives in core so that the memory, file and SQL engines refuse
// identical inputs identically, which is what makes the first usable as a
// test double for the other two.
func (p PolicyValue) Validate() error {
	//: a negative count, size or duration is not a bound: the caller meant
	//: something, and no engine can guess what.
	if field, value, ok := p.firstNegative(); !ok {
		//: StreamMisconfigured, naming the field.
		return refused(field, value)
	}
	//: a wait with nothing to wait under reads like backpressure and is none.
	if p.BlockTimeout > 0 && p.MaxBytes == 0 {
		//: StreamMisconfigured, naming the field.
		return refused("BlockTimeout", int64(p.BlockTimeout))
	}
	//: a ceiling below one record makes every Append wait, then fail.
	if p.MaxBytes > 0 && p.MaxBytes < int64(p.Normalized().MaxRecordBytes) {
		//: StreamMisconfigured, naming the field.
		return refused("MaxBytes", p.MaxBytes)
	}
	//: every remaining value has one reading.
	return nil
}

// firstNegative returns the first field holding a negative value, its value,
// and false; or true when none does.
func (p PolicyValue) firstNegative() (field string, value int64, ok bool) {
	fields := [...]struct {
		name  string
		value int64
	}{
		{"Partitions", int64(p.Partitions)},
		{"MaxRecordBytes", int64(p.MaxRecordBytes)},
		{"RetainFor", int64(p.RetainFor)},
		{"RetainBytes", p.RetainBytes},
		{"MaxBytes", p.MaxBytes},
		{"BlockTimeout", int64(p.BlockTimeout)},
		{"SegmentBytes", p.SegmentBytes},
		{"CompactAfter", int64(p.CompactAfter)},
	}
	//: in declaration order, so the refusal names the first mistake a reader sees.
	for _, f := range fields {
		//: negative: refused.
		if f.value < 0 {
			return f.name, f.value, false
		}
	}
	//: nothing negative.
	return "", 0, true
}

// refused builds the StreamMisconfigured refusal of one field.
func refused(field string, value int64) error {
	//: the field and its value, never anything else.
	return errs.Wrap(StreamMisconfigured, errs.WrapParams{},
		errs.String("field", field), errs.Int64("value", value))
}

// Normalized returns the policy with its clamped fields resolved, so that
// every engine reads the same numbers rather than each applying the same
// default. It does NOT validate: call [PolicyValue.Validate] first, at
// construction.
func (p PolicyValue) Normalized() PolicyValue {
	//: zero partitions is the one partition every stream has.
	if p.Partitions == 0 {
		p.Partitions = 1
	}
	//: zero is the documented request for the default record bound.
	if p.MaxRecordBytes == 0 {
		p.MaxRecordBytes = DefaultMaxRecordBytes
	}
	//: zero is the documented request for the default segment size.
	if p.SegmentBytes == 0 {
		p.SegmentBytes = DefaultSegmentBytes
	}
	//: the values every engine enforces.
	return p
}
