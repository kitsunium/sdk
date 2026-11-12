package stream

import (
	"context"
	"crypto/sha256"
	stdsql "database/sql"
	"errors"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// Commit records next as the offset group reads next on partition, in the
// transaction its context carries — so a handler that commits on its own
// transaction's context commits its effect and its offset together — or on
// the pool.
//
// The partition's next offset is read first, unlocked: it only ever grows, so
// a next at or below the value read is at or below the true one.
func (s *sqlStream) Commit(
	ctx context.Context, group string, partition int, next corestream.Offset,
) error {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return misconfigured("group")
	}
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return refused
	}
	head, err := s.headNext(ctx, partition)
	//: the head could not be read.
	if err != nil {
		//: StreamBackendFailed.
		return err
	}
	//: a cursor past every record is one from another stream.
	if next > head {
		//: OffsetAhead, with both offsets.
		return offsetAhead(next, head)
	}
	digest := sha256.Sum256([]byte(group))
	//: one statement: a savepoint inside the caller's transaction.
	return s.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
		_, execErr := ex.ExecContext(txCtx, s.stmts.commit, digest[:], int64(partition), int64(next), []byte(group), int64(next))
		//: kept, below the first retained offset included: the next Read says so.
		if execErr != nil {
			//: StreamBackendFailed, the driver's text withheld.
			return s.failed("commit a cursor", execErr)
		}
		//: committed.
		return nil
	})
}

// headNext reads partition's next offset, zero for a partition never written.
func (s *sqlStream) headNext(ctx context.Context, partition int) (corestream.Offset, error) {
	ex, _ := s.tx.join.Join(ctx)
	var next int64
	err := ex.QueryRowContext(ctx, s.stmts.headNext, int64(partition)).Scan(&next)
	//: no head row: nothing appended yet.
	if errors.Is(err, stdsql.ErrNoRows) {
		return 0, nil
	}
	//: the statement did not complete.
	if err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return 0, s.failed("read the head", err)
	}
	//: the next offset.
	return corestream.Offset(next), nil
}

// Committed returns the offset group committed last on partition.
func (s *sqlStream) Committed(
	ctx context.Context, group string, partition int,
) (corestream.Offset, error) {
	//: a cursor belongs to a named group.
	if group == "" {
		//: StreamMisconfigured, naming the argument.
		return 0, misconfigured("group")
	}
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return 0, refused
	}
	digest := sha256.Sum256([]byte(group))
	ex, _ := s.tx.join.Join(ctx)
	var next int64
	err := ex.QueryRowContext(ctx, s.stmts.committed, digest[:], int64(partition)).Scan(&next)
	//: never a zero: where a new group starts is the reader's decision.
	if errors.Is(err, stdsql.ErrNoRows) {
		//: UnknownGroup.
		return 0, unknownGroup(group, partition)
	}
	//: the statement did not complete.
	if err != nil {
		//: StreamBackendFailed, the driver's text withheld.
		return 0, s.failed("read a cursor", err)
	}
	//: the group's cursor.
	return corestream.Offset(next), nil
}
