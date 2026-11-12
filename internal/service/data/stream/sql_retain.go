package stream

import (
	"context"
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// Trim makes before the first offset partition retains.
//
// The head's floor is raised in one statement under the head's lock, so every
// record below before is unreadable from the commit of that statement — at
// once outside a transaction, at the caller's commit inside one. The rows
// themselves are then deleted in bounded statements, each a savepoint of the
// caller's transaction or a statement of its own: a failure there leaves rows
// nobody can read, which the next Trim or age sweep deletes.
func (s *sqlStream) Trim(ctx context.Context, partition int, before corestream.Offset) error {
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	raised := false
	floorErr := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
		var err error
		raised, err = s.raiseFloor(txCtx, ex, partition, before)
		//: raised, unchanged, OffsetAhead or StreamBackendFailed.
		return err
	})
	//: nothing moved.
	if floorErr != nil {
		//: OffsetAhead, StreamBackendFailed, or the transactor's verdict.
		return floorErr
	}
	//: a before at or below the floor changes nothing and frees nothing.
	if !raised {
		return nil
	}
	//: the storage follows the floor.
	return s.release(ctx, partition, int64(before))
}

// raiseFloor locks partition's head, refuses a before past its next offset,
// and raises its floor to before when that is higher. It reports whether the
// floor moved.
func (s *sqlStream) raiseFloor(
	ctx context.Context, ex coresql.Executor, partition int, before corestream.Offset,
) (bool, error) {
	//: created when missing, and held until the transaction ends.
	if _, err := ex.ExecContext(ctx, s.stmts.lockHead, int64(partition)); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return false, s.failed("lock the head", err)
	}
	var next, first, last int64
	//: the head this transaction holds.
	if err := ex.QueryRowContext(ctx, s.stmts.head, int64(partition)).Scan(&next, &first, &last); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return false, s.failed("read the head", err)
	}
	//: a record that does not exist yet.
	if before > corestream.Offset(next) {
		//: OffsetAhead, with both offsets.
		return false, offsetAhead(before, corestream.Offset(next))
	}
	//: at or below the floor changes nothing.
	if before <= corestream.Offset(first) {
		return false, nil
	}
	//: trimmed; the next offset does not move.
	if _, err := ex.ExecContext(ctx, s.stmts.floor, int64(before), int64(partition)); err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return false, s.failed("raise the floor", err)
	}
	//: moved.
	return true, nil
}

// release deletes partition's stored records below target, at most
// sqlOffsetsPerDelete offsets per statement, from the lowest stored one up.
// Every record it deletes is already unreadable.
func (s *sqlStream) release(ctx context.Context, partition int, target int64) error {
	//: one bounded step at a time, until nothing below target is stored.
	for {
		done := false
		stepErr := s.run(ctx, true, func(txCtx context.Context, ex coresql.Executor) error {
			var lowest stdsql.NullInt64
			//: where the stored records start.
			if err := ex.QueryRowContext(txCtx, s.stmts.lowest, int64(partition)).Scan(&lowest); err != nil {
				//: StreamBackendFailed, the driver's text withheld.
				return s.failed("find the lowest record", err)
			}
			//: nothing stored below target.
			if !lowest.Valid || lowest.Int64 >= target {
				done = true
				return nil
			}
			upper := min(target, lowest.Int64+sqlOffsetsPerDelete)
			_, err := ex.ExecContext(txCtx, s.stmts.release, int64(partition), upper)
			//: one bounded step.
			if err != nil {
				//: StreamBackendFailed, the driver's text withheld.
				return s.failed("release records", err)
			}
			//: deleted.
			return nil
		})
		//: a step that failed stops the release; the rows stay unreadable.
		if stepErr != nil || done {
			//: nil once nothing below target is stored.
			return stepErr
		}
	}
}

// sweep releases partition's records past RetainFor once its last sweep by
// this engine is older than the sweep period. They are unreadable already —
// every read filters them by age — so a sweep only gives their storage back,
// and a sweep that fails is retried by the next one due: an Append that
// committed is not reported as failed for it.
func (s *sqlStream) sweep(ctx context.Context, partition int, now time.Time) {
	//: no age bound frees nothing on its own.
	if s.policy.RetainFor == 0 {
		return
	}
	last := s.swept[partition].Load()
	period := min(s.policy.RetainFor, sqlSweepEvery)
	//: swept recently enough.
	if now.UnixNano()-last < int64(period) {
		return
	}
	//: another goroutine of this engine took this sweep.
	if !s.swept[partition].CompareAndSwap(last, now.UnixNano()) {
		return
	}
	_, first, err := s.headAndLowest(ctx, s.stmts.bounds, "read the bounds", s.cutoff(now), int64(partition))
	//: the bound could not be read: the next sweep due tries again.
	if err != nil {
		s.swept[partition].Store(last)
		return
	}
	//: the expired prefix; on a failure the next sweep due tries again.
	if s.release(ctx, partition, int64(first)) != nil {
		s.swept[partition].Store(last)
	}
}
