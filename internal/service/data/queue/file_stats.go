package queue

import (
	"context"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
)

// Stats counts the three state directories by their names, opening no file
// (core/data/queue.Counter): the counts are the entries the grammar accepts,
// the oldest instant the smallest enqueue instant a live name carries, and
// the next due the smallest visibility instant a queued name carries.
func (b *fileBroker) Stats(ctx context.Context) (corequeue.StatsValue, error) {
	//: a cancelled caller.
	if ctx.Err() != nil {
		//: the caller's deadline.
		return corequeue.StatsValue{}, ctx.Err()
	}
	var stats corequeue.StatsValue
	var oldest int64
	//: queued, then in flight: the same walk with a different grammar.
	for _, dir := range []string{dirReady, dirInflight} {
		//: the medium refused.
		if listErr := b.countLive(dir, &stats, &oldest); listErr != nil {
			//: QueueBackendFailed.
			return corequeue.StatsValue{}, listErr
		}
	}
	dead, deadErr := b.countDead()
	//: the medium refused.
	if deadErr != nil {
		//: QueueBackendFailed.
		return corequeue.StatsValue{}, deadErr
	}
	stats.Dead = dead
	//: no live message has no oldest one.
	if oldest != 0 {
		stats.OldestEnqueuedAt = time.Unix(0, oldest)
	}
	//: a snapshot of the names.
	return stats, nil
}

// countLive adds to stats the messages the live state directory dir names —
// queued or in flight — and lowers oldest to the earliest enqueue instant
// among them.
func (b *fileBroker) countLive(dir string, stats *corequeue.StatsValue, oldest *int64) error {
	entries, listErr := b.namesOf(dir)
	//: the medium refused.
	if listErr != nil {
		//: QueueBackendFailed.
		return listErr
	}
	//: every name the state's grammar accepts.
	for _, base := range entries {
		name, ok := parseLive(dir, base)
		//: a temporary, a stray.
		if !ok {
			continue
		}
		*oldest = earliest(*oldest, name.EnqueuedAt)
		//: in flight: counted, no visibility to report.
		if dir == dirInflight {
			stats.Leased++
			continue
		}
		//: each level sorts by visibility, so the next due is the earliest
		//: among the levels' — the earliest of all is as cheap to keep.
		if stats.Queued == 0 || name.AtTime().Before(stats.NextDueAt) {
			stats.NextDueAt = name.AtTime()
		}
		stats.Queued++
	}
	//: every name counted.
	return nil
}

// countDead counts the dead-letter records, as DeadLetters lists them.
func (b *fileBroker) countDead() (int64, error) {
	dead, deadErr := b.namesOf(dirDead)
	//: the medium refused.
	if deadErr != nil {
		//: QueueBackendFailed.
		return 0, deadErr
	}
	var count int64
	//: every record, as DeadLetters lists them.
	for _, base := range dead {
		//: a temporary, a stray.
		if _, named := parseDead(base); named {
			count++
		}
	}
	//: how many.
	return count, nil
}
