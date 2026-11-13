package stream

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/fs/flock"
	"github.com/kitsunium/sdk/internal/kernel/fs/seglog"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	corevfs "github.com/kitsunium/sdk/internal/core/data/vfs"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	svcvfs "github.com/kitsunium/sdk/internal/service/data/vfs"
)

// fileStream is a stream kept in a directory: one kernel/fs/seglog log per
// partition, and one file per consumer group's cursor.
//
// # Durability
//
// An Append is one seglog batch — its records, and a drop record when
// retention moved — written under one Sync: two fdatasyncs whatever the
// number of records, so a crash keeps all of the batch or none of it.
//
// # Retention
//
// Every bound is applied to the offset first, at once: a record past
// RetainFor, beyond RetainBytes or below a Trim is unreadable from that
// instant. The storage follows in whole segments: the Append or the Trim
// that moves the first offset records it in its batch, and every sealed
// segment holding only records below it is unlinked after that batch's sync
// — at the next rotation for the segment being written. The age bound is
// computed at read time, as the memory engine's is, and applied by the next
// Append, Trim or backpressure wait.
//
// # One engine per directory
//
// A segmented log has one writer, so the engine holds an exclusive lock on
// <Dir>/lock for its life (kernel/fs/flock): a second open of the directory,
// in this process or another, is refused until Close. The Waker of the open
// engine is therefore the directory's.
type fileStream struct {
	clk     clock.Timed
	cursors corevfs.FullFS
	lock    *os.File
	// wake is closed by every Append (corestream.Waker).
	wake *wakeSignal
	// room is closed whenever records are released, which an Append
	// waiting under PolicyValue.MaxBytes waits for.
	room   *wakeSignal
	parts  []*filePart
	policy corestream.PolicyValue
	// mu guards the four below. It is taken before a partition's mu, never
	// after.
	mu sync.Mutex
	// held is the bytes every partition retains, the figure MaxBytes
	// bounds, the batches being written included.
	held int64
	// last is the latest instant a batch was stamped with, in Unix
	// nanoseconds: AppendedAt never goes backwards, across a restart too.
	last int64
	// turn is the partition the next unkeyed batch goes to.
	turn   int
	closed bool
}

// NewFile opens the stream kept in cfg.Dir, creating it when absent, and
// recovers it: every partition's segmented log, the batches and instants
// its records carry, and the retention they were under.
//
// It refuses a policy it cannot honour with the core's Validate, as every
// engine does, and a MaxRecordBytes above what one segment record holds; a
// directory it cannot trust, or one another engine holds open, with
// StreamDirectoryUnusable; a directory holding more partitions than the
// policy counts with StreamMisconfigured; and a segment it cannot trust with
// the segmented log's own refusal under StreamBackendFailed. The stream it
// returns also answers [corestream.Waker], [corestream.Seeker],
// [corestream.Trimmer], [corestream.Compactor], [corestream.Committer] and
// io.Closer; Close releases the directory. A compaction rewrites the
// segments holding a record it drops, each published by a rename then a
// directory sync, a dropped record leaving its offset and instant behind.
//
// The groups' cursors are published through vfs's disk filesystem, so
// where vfs has none — Windows, illumos and Solaris, which lack a
// flushable directory handle or a mode that is an access rule (ADR 0018)
// — NewFile refuses with proc.UnsupportedPlatform, under
// StreamBackendFailed, before it reads the policy or the directory, and
// returns no stream.
func NewFile(cfg FileConfig) (s corestream.Stream, err error) {
	//: a platform with no disk filesystem for the cursors, before any input:
	//: the same answer on every refused platform, whatever the directory.
	if refused := platformRefusal(); refused != nil {
		//: UnsupportedPlatform under StreamBackendFailed.
		return nil, refused
	}
	//: the shared guard, so every engine refuses identical inputs identically.
	if invalid := cfg.Policy.Validate(); invalid != nil {
		//: StreamMisconfigured, naming the field.
		return nil, invalid
	}
	policy := cfg.Policy.Normalized()
	//: a record one segment record could not hold would fail half way through a batch.
	if policy.MaxRecordBytes > maxSegmentPayload-recordOverhead {
		//: StreamMisconfigured, naming the field.
		return nil, misconfigured("MaxRecordBytes")
	}
	//: the directory, checked before anything is written into it.
	if unusable := prepareRoot(cfg.Dir); unusable != nil {
		//: StreamDirectoryUnusable or StreamBackendFailed.
		return nil, unusable
	}
	stream := &fileStream{
		clk:    clockOrSystem(cfg.Clock),
		wake:   newWakeSignal(),
		room:   newWakeSignal(),
		policy: policy,
	}
	//: whatever was opened is closed on the way out of a refusal.
	defer func() {
		//: a stream returned is the caller's to close.
		if err != nil {
			abandonStream(stream)
		}
	}()
	//: the lock first: nothing is read that another engine may be writing.
	if stream.lock, err = lockDir(cfg.Dir); err != nil {
		//: StreamDirectoryUnusable or StreamBackendFailed.
		return nil, err
	}
	//: a shrunk partition count would orphan the records of the others.
	if err = checkPartitions(cfg.Dir, policy.Partitions); err != nil {
		//: StreamMisconfigured or StreamBackendFailed.
		return nil, err
	}
	//: the cursors and the partitions.
	if err = stream.open(cfg.Dir); err != nil {
		//: the first refusal.
		return nil, err
	}
	//: recovered.
	return stream, nil
}

// open opens the cursors' filesystem and every partition's log, and indexes
// what the logs hold.
func (m *fileStream) open(dir string) error {
	cursors, err := prepareSub(dir, cursorsDir)
	//: an untrusted cursors' directory.
	if err != nil {
		//: StreamDirectoryUnusable or StreamBackendFailed.
		return err
	}
	//: the cursors are published atomically, through vfs.
	if m.cursors, err = svcvfs.NewOS(cursors); err != nil {
		//: the filesystem's refusal.
		return backendFailed("open", cursors, err)
	}
	//: every partition, in order.
	for p := range m.policy.Partitions {
		part, partErr := m.openPart(dir, p)
		//: the first partition that cannot be served refuses the stream.
		if partErr != nil {
			//: its refusal.
			return partErr
		}
		m.parts = append(m.parts, part)
	}
	//: retained bytes and the latest instant, from the indexes.
	for _, part := range m.parts {
		m.held += part.between(part.kept, part.next())
	}
	//: open.
	return nil
}

// openPart opens and indexes partition p's log, under p<p>/.
func (m *fileStream) openPart(dir string, p int) (*filePart, error) {
	sub, err := prepareSub(dir, partitionDir(p))
	//: an untrusted partition directory.
	if err != nil {
		//: StreamDirectoryUnusable or StreamBackendFailed.
		return nil, err
	}
	log, err := seglog.Open(sub, m.policy.SegmentBytes)
	//: a log seglog cannot trust is not served.
	if err != nil {
		//: its refusal, in this engine's trail.
		return nil, backendFailed("open", sub, err)
	}
	part := &filePart{log: log}
	latest, err := part.rebuild()
	//: a record this engine cannot read back.
	if err != nil {
		abandonLog(log)
		//: StreamBackendFailed.
		return nil, err
	}
	m.last = max(m.last, latest)
	part.claim(part.bounded(part.kept, part.lastBatch(), 0, m.policy.RetainBytes))
	//: indexed, the byte bound applied as it was before the restart.
	return part, nil
}

// partitionDir names partition p's directory.
func partitionDir(p int) string {
	//: p0, p1, ….
	return "p" + strconv.Itoa(p)
}

// lockDir takes the exclusive lock on dir's lock file without waiting.
func lockDir(dir string) (*os.File, error) {
	name := filepath.Join(dir, lockName)
	file, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, fileMode) //nolint:gosec // dir is the caller's, checked by prepareRoot.
	//: no lock file, no exclusion.
	if err != nil {
		//: the medium refused.
		return nil, backendFailed("open", name, err)
	}
	held, err := flock.TryLock(file)
	//: a platform with no file lock cannot keep a second writer out.
	if err != nil {
		abandon(file.Close())
		//: StreamDirectoryUnusable when the platform lacks it, the errno otherwise.
		if errors.Is(err, errors.ErrUnsupported) {
			//: refused where it is wired.
			return nil, directoryUnusable(dir, "no-file-lock", "")
		}
		//: the kernel refused the call.
		return nil, backendFailed("lock", name, err)
	}
	//: another engine has it open.
	if !held {
		abandon(file.Close())
		//: StreamDirectoryUnusable, in use.
		return nil, directoryUnusable(dir, "in-use", "")
	}
	//: held until Close.
	return file, nil
}

// platformRefusal is vfs's refusal of this platform, under
// StreamBackendFailed, or nil where vfs has a disk filesystem. vfs.NewOS
// judges the platform before the path, so the empty path is never opened:
// where the platform is supported it answers the path's refusal, ignored
// here. Checked first, it makes every refused platform answer
// UNSUPPORTED_PLATFORM, never a refusal of the directory, its lock or its
// mode that a later step would give there first (illumos has no file lock).
func platformRefusal() error {
	_, err := svcvfs.NewOS("")
	//: the platform refused: no directory would make the engine work here.
	if errors.Is(err, coreproc.UnsupportedPlatform) {
		//: the platform's refusal stays the origin, so errs.HasCode finds it.
		return backendFailed("open", "", err)
	}
	//: a disk filesystem exists here.
	return nil
}

// checkPartitions refuses a directory holding a partition at or past count.
func checkPartitions(dir string, count int) error {
	entries, err := os.ReadDir(dir)
	//: a directory that cannot be listed cannot be checked.
	if err != nil {
		//: the medium refused.
		return backendFailed("readdir", dir, err)
	}
	//: every p<digits> entry.
	for _, entry := range entries {
		digits, isPart := strings.CutPrefix(entry.Name(), "p")
		n, convErr := strconv.Atoi(digits)
		//: another entry, or a partition the policy still counts.
		if !isPart || convErr != nil || n < count {
			continue
		}
		//: its records would no longer be reachable.
		return misconfigured("Partitions")
	}
	//: every partition on disk is counted.
	return nil
}

// abandon drops the error of a release on a path that is already failing:
// the refusal being returned is the one that matters.
func abandon(failure error) {
	if failure == nil {
		return
	}
	// The release failed on a path that already has its own answer.
}

// abandonLog closes a log a refusal leaves behind.
func abandonLog(log *seglog.Log) {
	//: the refusal being returned is the one that matters.
	abandon(log.Close())
}

// abandonStream releases what a refused NewFile opened.
func abandonStream(m *fileStream) {
	//: the refusal being returned is the one that matters.
	abandon(m.Close())
}

// Partitions returns how many partitions the stream has.
func (m *fileStream) Partitions() int {
	//: fixed at construction, so no lock.
	return len(m.parts)
}

// Append writes records to one partition as one batch, under one sync.
func (m *fileStream) Append(
	ctx context.Context, records []corestream.RecordValue,
) (corestream.AppendValue, error) {
	//: a caller that has already given up writes nothing.
	if ctx.Err() != nil {
		//: the caller's own cause.
		return corestream.AppendValue{}, context.Cause(ctx)
	}
	size, refused := admit(records, m.policy.MaxRecordBytes)
	//: the whole batch is refused before anything is written.
	if refused != nil {
		//: InvalidBatchSize or RecordTooLarge.
		return corestream.AppendValue{}, refused
	}
	partition, keyed, mixed := keyedPartition(records, len(m.parts))
	//: a batch is one partition's.
	if mixed != nil {
		//: PartitionsMixed.
		return corestream.AppendValue{}, mixed
	}
	//: room under MaxBytes, reserved for this batch; the turn of an unkeyed one.
	partition, refused = m.reserve(ctx, size, partition, keyed)
	if refused != nil {
		//: StreamFull, a closed stream, or the context's cause.
		return corestream.AppendValue{}, refused
	}
	part := m.parts[partition]
	part.wmu.Lock()
	appended, freed, failed := m.write(part, partition, records, size)
	part.wmu.Unlock()
	m.release(freed, failed != nil, size)
	//: nothing is acknowledged that the device does not hold.
	if failed != nil {
		//: StreamBackendFailed.
		return corestream.AppendValue{}, failed
	}
	m.wake.fire()
	//: where the batch landed.
	return appended, nil
}

// write encodes records into part's log, syncs them as one batch with the
// retention they move, and indexes them. The caller holds part.wmu.
func (m *fileStream) write(
	part *filePart, partition int, records []corestream.RecordValue, size int64,
) (appended corestream.AppendValue, freed int64, err error) {
	//: a partition a failure stopped takes nothing more.
	if part.broken != nil {
		//: the failure that stopped it.
		return appended, 0, part.broken
	}
	first := part.next()
	stamp := m.stamp()
	sizes := make([]int64, len(records))
	//: one seglog record per stream record; the log copies each.
	for i, record := range records {
		kind := kindTail
		//: the head opens the batch.
		if i == 0 {
			kind = kindHead
		}
		part.scratch = encodeRecord(part.scratch[:0], stamp, record)
		sizes[i] = recordBytes(record)
		//: Append refuses only a closed or failed log here; its batch is spoilt.
		if appendErr := part.log.Append(uint64(first)+uint64(i), kind, part.scratch); appendErr != nil {
			part.broken = backendFailed("append", "", appendErr)
			//: StreamBackendFailed.
			return appended, 0, part.broken
		}
	}
	part.mu.RLock()
	claim := part.first(timeOf(stamp), m.policy.RetainFor)
	claim = part.bounded(claim, first, size, m.policy.RetainBytes)
	part.mu.RUnlock()
	//: one sync for the batch, the drop record riding in it when retention moved.
	if syncErr := m.syncBatch(part, claim); syncErr != nil {
		part.broken = backendFailed("sync", "", syncErr)
		//: StreamBackendFailed: the batch may or may not be on the device.
		return appended, 0, part.broken
	}
	part.mu.Lock()
	freed = part.index(stamp, sizes, claim)
	part.mu.Unlock()
	//: the batch's range.
	return corestream.AppendValue{
		Partition: partition,
		First:     first,
		Last:      first + corestream.Offset(len(records)-1),
	}, freed, nil
}

// syncBatch makes part's pending batch durable: with a drop record when claim
// is past the log's first index, which also unlinks the sealed segments it
// empties, with a plain sync otherwise.
func (m *fileStream) syncBatch(part *filePart, claim corestream.Offset) error {
	first, _ := part.log.Bounds()
	//: retention moved past what the log holds.
	if uint64(claim) > first {
		//: the drop rides in the batch, under the same sync.
		return part.log.DropBefore(uint64(claim))
	}
	//: the batch alone.
	return part.log.Sync()
}

// release gives back what an Append's retention freed and, when the
// Append failed, the room it had reserved.
func (m *fileStream) release(freed int64, failed bool, size int64) {
	m.mu.Lock()
	m.held -= freed
	//: a batch that was not written holds nothing.
	if failed {
		m.held -= size
	}
	m.mu.Unlock()
	//: an Append waiting for room looks again.
	if freed > 0 || failed {
		m.room.fire()
	}
}

// stamp returns the instant, in Unix nanoseconds, the batch being appended
// is stamped with: the clock's, or the last stamp when the clock went
// backwards. The caller holds the partition's wmu, so a partition's batches
// are stamped in their order.
func (m *fileStream) stamp() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clk.Now().UnixNano()
	//: never before the previous batch.
	m.last = max(m.last, now)
	//: the batch's one instant.
	return m.last
}

// timeOf returns the instant of a stamp, in UTC.
func timeOf(stamp int64) time.Time {
	//: the instant the segments keep.
	return time.Unix(0, stamp).UTC()
}

// usable refuses a call on a closed stream. The caller holds no lock.
func (m *fileStream) usable() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a closed stream answers nothing.
	if m.closed {
		//: the segmented log's word for it, in this engine's trail.
		return backendFailed("closed", "", seglog.Closed)
	}
	//: open.
	return nil
}

// Close closes every partition's log and the cursors' filesystem, then
// releases the directory's lock. It reports the first failure; a second
// Close returns nil.
func (m *fileStream) Close() error {
	m.mu.Lock()
	//: once.
	if m.closed {
		m.mu.Unlock()
		//: already released.
		return nil
	}
	m.closed = true
	m.mu.Unlock()
	//: every release runs, whatever an earlier one reported.
	return firstError(m.closeParts(), m.closeCursors(), m.releaseLock())
}

// firstError is the first non-nil of errs, nil when there is none.
func firstError(errs ...error) error {
	//: the first failure is the one reported.
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	//: nothing failed.
	return nil
}

// closeParts closes every partition's log, each under its writers' lock so
// no batch is cut, and returns the first failure.
func (m *fileStream) closeParts() error {
	var first error
	//: every partition.
	for _, part := range m.parts {
		part.wmu.Lock()
		closeErr := part.log.Close()
		part.wmu.Unlock()
		//: the first failure is the one reported.
		if closeErr != nil && first == nil {
			first = backendFailed("close", "", closeErr)
		}
	}
	return first
}

// closeCursors closes the cursors' root handle.
func (m *fileStream) closeCursors() error {
	//: the cursors' root handle.
	if closer, ok := m.cursors.(io.Closer); ok {
		//: the failure, if any.
		if closeErr := closer.Close(); closeErr != nil {
			return backendFailed("close", cursorsDir, closeErr)
		}
	}
	return nil
}

// releaseLock releases the directory's lock last, so no other engine opens
// a directory still being closed, and returns the first failure.
func (m *fileStream) releaseLock() error {
	//: no lock held.
	if m.lock == nil {
		return nil
	}
	//: an explicit unlock, so a failure to release is reported.
	unlockErr := flock.Unlock(m.lock)
	//: the descriptor, closed whether or not the unlock failed.
	closeErr := m.lock.Close()
	//: the first failure is the one reported.
	if unlockErr != nil {
		return backendFailed("unlock", lockName, unlockErr)
	}
	//: the descriptor's own failure.
	if closeErr != nil {
		return backendFailed("close", lockName, closeErr)
	}
	return nil
}
