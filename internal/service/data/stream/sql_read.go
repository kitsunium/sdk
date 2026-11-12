package stream

import (
	"context"
	stdsql "database/sql"
	"errors"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// checkPartition refuses a partition outside [0, Partitions()).
func (s *sqlStream) checkPartition(partition int) error {
	//: the one range every partition argument is held to.
	if partition < 0 || partition >= s.policy.Partitions {
		//: UnknownPartition.
		return unknownPartition(partition, s.policy.Partitions)
	}
	//: a partition of this stream.
	return nil
}

// Read returns up to maxEntries entries of partition from offset from on, in
// ONE unlocked statement on the executor the context's transaction joins —
// the pool outside one.
//
// The statement answers the head — its next offset, its floor and the
// highest stored offset past the age bound, which give the first readable
// offset (firstReadable) — beside the readable records from from on. A
// compaction leaves holes the rows simply do not hold, so a read inside one
// answers the records after it and never OFFSET_TRIMMED.
func (s *sqlStream) Read(
	ctx context.Context, partition int, from corestream.Offset, maxEntries int,
) ([]corestream.EntryValue, error) {
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return nil, refused
	}
	//: a loop asking for nothing would process nothing forever.
	if maxEntries <= 0 {
		//: InvalidBatchSize.
		return nil, invalidBatchSize("max", maxEntries)
	}
	ex, _ := s.tx.join.Join(ctx)
	cutoff := s.cutoff(s.clk.Now())
	rows, err := ex.QueryContext(ctx, s.stmts.read, cutoff, cutoff, int64(from), int64(partition), int64(maxEntries))
	//: the statement did not run.
	if err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return nil, s.failed("read records", err)
	}
	entries, bounds, err := s.scanEntries(rows, partition, maxEntries)
	//: a row the driver could not hand over.
	if err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return nil, s.failed("read records", err)
	}
	//: a record that does not exist yet.
	if from > bounds.Next {
		//: OffsetAhead, with both offsets.
		return nil, offsetAhead(from, bounds.Next)
	}
	//: below the first readable offset: trimmed or expired.
	if from < bounds.First {
		//: OffsetTrimmed, the first readable offset beside the one asked.
		return nil, offsetTrimmed(from, bounds.First)
	}
	//: the reader's own copy; empty at the next offset.
	return entries, nil
}

// scannedRows is the part of *sql.Rows scanEntries reads a result through:
// the cursor, its failure, its columns and its release.
type scannedRows interface {
	Close() error
	Err() error
	Next() bool
	Scan(dest ...any) error
}

// scanEntries reads the rows of the read statement: the head on each, a
// record on each but the lone row of a partition with none readable. A
// partition never written has no head row, and its bounds are zero.
func (s *sqlStream) scanEntries(
	rows scannedRows, partition, maxEntries int,
) (entries []corestream.EntryValue, bounds corestream.BoundsValue, err error) {
	defer func() {
		//: the first failure is the one reported.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	//: one row per record, or one row with none.
	for rows.Next() {
		var (
			head, floor            int64
			expired, offset, stamp stdsql.NullInt64
			key, payload           []byte
		)
		//: the seven columns, the last four NULL with no record.
		if scanErr := rows.Scan(&head, &floor, &expired, &offset, &stamp, &key, &payload); scanErr != nil {
			//: the row could not be read.
			return nil, corestream.BoundsValue{}, scanErr
		}
		bounds = firstReadable(head, floor, expired)
		//: the lone row of a partition with nothing readable.
		if !offset.Valid {
			continue
		}
		//: sized on the first record.
		if entries == nil {
			entries = make([]corestream.EntryValue, 0, maxEntries)
		}
		entries = append(entries, corestream.EntryValue{
			Partition:  partition,
			Offset:     corestream.Offset(offset.Int64),
			AppendedAt: time.Unix(0, stamp.Int64),
			Record:     corestream.RecordValue{Key: nonEmpty(key), Payload: nonEmpty(payload)},
		})
	}
	//: an iteration that stopped on a failure.
	return entries, bounds, rows.Err()
}

// nonEmpty reads an empty column back as nil, as the memory engine keeps an
// empty key or payload.
func nonEmpty(b []byte) []byte {
	//: NULL and empty alike.
	if len(b) == 0 {
		return nil
	}
	//: the driver's copy, the reader's own.
	return b
}

// Bounds returns partition's first retained offset and its next one.
func (s *sqlStream) Bounds(ctx context.Context, partition int) (corestream.BoundsValue, error) {
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.BoundsValue{}, refused
	}
	//: the retained range, or StreamBackendFailed.
	return s.boundsAt(ctx, partition, s.cutoff(s.clk.Now()))
}

// boundsAt reads partition's bounds under the age cutoff: one statement on
// the executor the context's transaction joins.
func (s *sqlStream) boundsAt(ctx context.Context, partition int, cutoff int64) (corestream.BoundsValue, error) {
	ex, _ := s.tx.join.Join(ctx)
	var head, floor int64
	var expired stdsql.NullInt64
	err := ex.QueryRowContext(ctx, s.stmts.bounds, cutoff, int64(partition)).Scan(&head, &floor, &expired)
	//: a partition never written has no head row: nothing, from zero.
	if errors.Is(err, stdsql.ErrNoRows) {
		return corestream.BoundsValue{}, nil
	}
	//: the statement did not complete.
	if err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return corestream.BoundsValue{}, s.failed("read the bounds", err)
	}
	//: the retained range.
	return firstReadable(head, floor, expired), nil
}

// firstReadable returns the bounds a head row gives: the next offset, and as
// the first the floor or the offset after the highest stored record past the
// age bound, whichever is higher. Every record above that one is younger, the
// instants following the offsets, so the holes a compaction left below or
// among the young records never move it.
func firstReadable(head, floor int64, expired stdsql.NullInt64) corestream.BoundsValue {
	first := floor
	//: an expired record still stored lifts the first offset past it.
	if expired.Valid {
		first = max(first, expired.Int64+1)
	}
	//: never past the next offset.
	return corestream.BoundsValue{First: corestream.Offset(min(first, head)), Next: corestream.Offset(head)}
}

// OffsetAt returns partition's first readable offset appended at or after t.
func (s *sqlStream) OffsetAt(
	ctx context.Context, partition int, t time.Time,
) (corestream.Offset, error) {
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	at := t.UnixNano()
	//: an instant before the epoch precedes every record.
	if t.Before(time.Unix(0, 0)) {
		at = 0
	}
	_, found, err := s.headAndLowest(ctx, s.stmts.offsetAt, "seek an instant", s.cutoff(s.clk.Now()), at, int64(partition))
	//: the statement did not complete.
	if err != nil {
		//: StreamBackendFailed.
		return 0, err
	}
	//: the first one at or after t, or next when t follows them all.
	return found, nil
}

// headAndLowest runs a statement answering the head's next offset and the
// lowest offset matching it, and resolves a partition with no head, or no
// matching record, to its next offset.
func (s *sqlStream) headAndLowest(
	ctx context.Context, statement, step string, args ...any,
) (next, lowest corestream.Offset, err error) {
	ex, _ := s.tx.join.Join(ctx)
	var head int64
	var low stdsql.NullInt64
	scanErr := ex.QueryRowContext(ctx, statement, args...).Scan(&head, &low)
	//: a partition never written has no head row: nothing, from zero.
	if errors.Is(scanErr, stdsql.ErrNoRows) {
		return 0, 0, nil
	}
	//: the statement did not complete.
	if scanErr != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return 0, 0, s.failed(step, scanErr)
	}
	next = corestream.Offset(head)
	//: no matching record is the next offset.
	if !low.Valid {
		return next, next, nil
	}
	//: the lowest match.
	return next, corestream.Offset(low.Int64), nil
}

// Wake returns the channel the next committed Append through any engine over
// this stream in this process closes.
func (s *sqlStream) Wake() <-chan struct{} {
	//: armed before the caller's Read, so an append in between is not missed.
	return s.wake.current()
}
