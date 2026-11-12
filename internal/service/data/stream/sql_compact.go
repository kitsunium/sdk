package stream

import (
	"context"
	stdsql "database/sql"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// sqlCompactRow is what a compaction reads of one row: never its payload,
// only whether it is empty.
type sqlCompactRow struct {
	key    string
	offset int64
	at     int64
	tomb   bool
}

// Compact removes from partition every compactable record a later one of its
// key supersedes, and every tombstone past its second window.
//
// It reads the readable rows old enough to be compacted a page at a time,
// decides in the engine — the same decision every engine makes —, then
// deletes the rows it dropped by offset, at most sqlCompactPerDelete a
// statement, each statement a savepoint of the caller's transaction or a
// statement of its own. A row a delete finds aged out is left to the age
// sweep, so the first readable offset never moves.
func (s *sqlStream) Compact(ctx context.Context, partition int) (corestream.CompactValue, error) {
	//: a partition of this stream.
	if refused := s.checkPartition(partition); refused != nil {
		//: UnknownPartition.
		return corestream.CompactValue{}, refused
	}
	//: zero never compacts.
	if s.policy.CompactAfter == 0 {
		//: nothing looked at.
		return corestream.CompactValue{}, nil
	}
	now := s.clk.Now()
	lines := compactLinesAt(now, s.policy.CompactAfter)
	rows, err := s.compactable(ctx, partition, now, lines)
	//: the rows could not be read.
	if err != nil {
		//: StreamBackendFailed.
		return corestream.CompactValue{}, err
	}
	dropped := planSQLCompaction(rows, lines)
	removed, err := s.deleteCompacted(ctx, partition, dropped)
	//: a delete that failed stops the compaction; what it deleted stays deleted.
	if err != nil {
		//: StreamBackendFailed, or the transactor's verdict.
		return corestream.CompactValue{}, err
	}
	//: what was looked at, kept and dropped.
	return compactResult(int64(len(rows)), removed), nil
}

// compactable reads partition's readable rows appended at or before the
// compaction's line, in offset order, a page at a time.
func (s *sqlStream) compactable(
	ctx context.Context, partition int, now time.Time, lines compactLines,
) ([]sqlCompactRow, error) {
	cutoff := s.cutoff(now)
	bounds, err := s.boundsAt(ctx, partition, cutoff)
	//: the bounds could not be read.
	if err != nil {
		//: StreamBackendFailed.
		return nil, err
	}
	var rows []sqlCompactRow
	//: a page at a time, from the first readable offset on.
	for from := int64(bounds.First); ; {
		page, pageErr := s.compactPage(ctx, partition, from, cutoff, lines.old.UnixNano())
		//: a page that could not be read.
		if pageErr != nil {
			//: StreamBackendFailed, the driver's text withheld.
			return nil, s.failed("read compactable records", pageErr)
		}
		rows = append(rows, page...)
		//: a short page is the last one.
		if len(page) < sqlCompactPage {
			//: every compactable row.
			return rows, nil
		}
		from = page[len(page)-1].offset + 1
	}
}

// compactPage reads one page of compactable rows from offset from on.
func (s *sqlStream) compactPage(
	ctx context.Context, partition int, from, cutoff, line int64,
) (page []sqlCompactRow, err error) {
	ex, _ := s.tx.join.Join(ctx)
	rows, err := ex.QueryContext(ctx, s.stmts.compactScan, int64(partition), from, cutoff, line, int64(sqlCompactPage))
	//: the statement did not run.
	if err != nil {
		//: the driver's error, withheld by the caller.
		return nil, err
	}
	defer func() {
		//: the first failure is the one reported.
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	//: one row per compactable record.
	for rows.Next() {
		row, scanErr := scanCompactRow(rows)
		//: the row could not be read.
		if scanErr != nil {
			return nil, scanErr
		}
		page = append(page, row)
	}
	//: an iteration that stopped on a failure.
	return page, rows.Err()
}

// scanCompactRow reads the row rows is at: offset, key, instant, and whether
// the payload is empty.
func scanCompactRow(rows scannedRows) (sqlCompactRow, error) {
	var (
		row   sqlCompactRow
		key   []byte
		empty stdsql.NullInt64
	)
	//: the four columns of the compaction scan.
	if err := rows.Scan(&row.offset, &key, &row.at, &empty); err != nil {
		return sqlCompactRow{}, err
	}
	row.key, row.tomb = string(key), empty.Int64 == 1
	return row, nil
}

// planSQLCompaction returns the offsets of the rows a compaction drops, in
// offset order: the decision compactLines makes on every engine.
func planSQLCompaction(rows []sqlCompactRow, lines compactLines) []int64 {
	last := make(map[string]int64, len(rows))
	//: the last compactable row of each key.
	for _, row := range rows {
		//: an unkeyed row is nobody's last.
		if row.key != "" {
			last[row.key] = row.offset
		}
	}
	var dropped []int64
	//: in offset order.
	for _, row := range rows {
		record := corestream.RecordValue{Key: []byte(row.key)}
		//: a payload that is not empty, its bytes not needed to decide.
		if !row.tomb {
			record.Payload = []byte{0}
		}
		//: a row that goes.
		if lines.drops(record, time.Unix(0, row.at), last[row.key] == row.offset) {
			dropped = append(dropped, row.offset)
		}
	}
	//: the offsets to delete.
	return dropped
}

// deleteCompacted deletes partition's rows at the dropped offsets, at most
// sqlCompactPerDelete a statement, and returns how many went.
func (s *sqlStream) deleteCompacted(ctx context.Context, partition int, dropped []int64) (int64, error) {
	var removed int64
	// One argument slice serves every statement: run finishes with it before
	// the next chunk starts.
	args := make([]any, 0, min(len(dropped), sqlCompactPerDelete)+2)
	//: one bounded statement at a time.
	for start := 0; start < len(dropped); start += sqlCompactPerDelete {
		chunk := dropped[start:min(start+sqlCompactPerDelete, len(dropped))]
		args = append(args[:0], int64(partition), s.cutoff(s.clk.Now()))
		//: one argument per offset.
		for _, offset := range chunk {
			args = append(args, offset)
		}
		err := s.run(ctx, false, func(txCtx context.Context, ex coresql.Executor) error {
			result, execErr := ex.ExecContext(txCtx, s.stmts.compactFor(len(chunk)), args...)
			//: the statement did not complete.
			if execErr != nil {
				//: StreamBackendFailed, the driver's text withheld.
				return s.failed("delete compacted records", execErr)
			}
			affected, countErr := result.RowsAffected()
			//: a driver that cannot count: the rows asked for.
			if countErr != nil {
				affected = int64(len(chunk))
			}
			removed += affected
			//: deleted.
			return nil
		})
		//: a step that failed stops the compaction.
		if err != nil {
			//: its refusal.
			return removed, err
		}
	}
	//: every dropped row.
	return removed, nil
}
