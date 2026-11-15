package queue_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The file broker keeps an index of ready/ and inflight/ (issue #811): on
// Linux, over a local filesystem, kept by inotify from the first Receive on;
// everywhere else read again under the listings' rule, which
// file_listing_external_test.go holds in both modes. These cases hold the
// watch itself — that it is taken where it can be, what an overflow of the
// kernel's queue and a directory moved away do to it — and the index under
// goroutines of one broker.

// maxOverflowEvents bounds the overflow case: past it, filling the kernel's
// queue would write more files than a unit test should.
const maxOverflowEvents int = 1 << 16

// requireWatched reports whether broker's directories are watched after a
// first look, and skips a case that needs a watch where there is none to
// take — after asserting that the platform is one that has none, or that the
// reason is the user's limit of instances or a filesystem another host
// changes, which a test machine may impose.
func requireWatched(t *testing.T, broker corequeue.Broker) {
	t.Helper()
	receiveNone(t, broker)
	watched, why := svcqueue.WatchStateForTest(broker)
	if watched {
		return
	}
	if runtime.GOOS == "linux" && !strings.Contains(why, "too many open files") &&
		!strings.Contains(why, "network or FUSE") {
		t.Fatalf("a broker over a local directory on Linux is not watched: %s", why)
	}
	t.Skip("no watch here: " + why)
}

// TestTheFileBrokerWatchesItsDirectoriesWhereTheKernelNamesTheirChanges pins
// where the index is kept by the kernel: on Linux, over a local filesystem,
// from the first Receive on; on every other platform never, with the reason;
// and never after Close, which releases the instance.
func TestTheFileBrokerWatchesItsDirectoriesWhereTheKernelNamesTheirChanges(t *testing.T) {
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: t.TempDir(), Policy: defaultPolicy()})
	if watched, why := svcqueue.WatchStateForTest(broker); watched || why != "" {
		t.Fatalf("before any Receive: watched=%v why=%q, want nothing decided", watched, why)
	}
	receiveNone(t, broker)
	watched, why := svcqueue.WatchStateForTest(broker)
	if runtime.GOOS != "linux" {
		if watched || why == "" {
			t.Fatalf("on %s: watched=%v why=%q, want unwatched, with the reason", runtime.GOOS, watched, why)
		}
		return
	}
	requireWatched(t, broker)
	if err := broker.(interface{ Close() error }).Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if watched, why = svcqueue.WatchStateForTest(broker); watched || why == "" {
		t.Fatalf("after Close: watched=%v why=%q, want the watch released", watched, why)
	}
}

// TestAWatchedIndexIsReadWholeAgainAfterTheKernelsQueueOverflows fills the
// kernel's queue past max_queued_events with names written straight into
// ready/ — the oldest of them written LAST, so its creation is among the
// events the overflow dropped — and the next Receive still leases it first:
// the overflow event asks for a full read, and the read finds it.
func TestAWatchedIndexIsReadWholeAgainAfterTheKernelsQueueOverflows(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the kernel's change queue is inotify's: there is none to overflow on " + runtime.GOOS)
	}
	limit := maxQueuedEvents(t)
	if limit > maxOverflowEvents {
		t.Skipf("max_queued_events is %d: overflowing it would write more than %d files", limit, maxOverflowEvents)
	}
	dir := t.TempDir()
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
	requireWatched(t, broker)

	base := time.Now().Add(-time.Hour).UnixNano()
	count := limit + 64
	for index := range count {
		// The last one written is visible first.
		at := base + int64(count-index)
		plant(t, filepath.Join(dir, "ready", svcqueue.ReadyNameForTest(at, at, fmt.Sprintf("%016x", index), 0)), nil)
	}
	oldest := fmt.Sprintf("%019d-%016x", base+1, count-1)

	if got := receiveOne(t, broker); got.Message.ID != oldest {
		t.Fatalf("Receive after an overflow delivered %q, want the oldest, written last: %q", got.Message.ID, oldest)
	}
	if watched, why := svcqueue.WatchStateForTest(broker); !watched {
		t.Fatalf("an overflow dropped the watch: %s", why)
	}
}

// TestAWatchWhoseDirectoryMovedFallsBackToTheListingsRule moves ready/ away
// and back under a watched broker: the watch is on the directory that moved,
// so it answers for it no longer, and the broker reads its directories under
// the listings' rule from then on — a publication made after the move is
// delivered all the same.
func TestAWatchWhoseDirectoryMovedFallsBackToTheListingsRule(t *testing.T) {
	dir := t.TempDir()
	consumer := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
	producer := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
	requireWatched(t, consumer)

	ready := filepath.Join(dir, "ready")
	if err := os.Rename(ready, ready+".moved"); err != nil {
		t.Fatalf("Rename(ready) = %v", err)
	}
	if err := os.Rename(ready+".moved", ready); err != nil {
		t.Fatalf("Rename(ready) back = %v", err)
	}
	published := publish(t, producer, "after-the-move")

	if got := receiveOne(t, consumer); got.Message.ID != published.ID {
		t.Fatalf("Receive delivered %q, want %q", got.Message.ID, published.ID)
	}
	if watched, why := svcqueue.WatchStateForTest(consumer); watched || !strings.Contains(why, "lost") {
		t.Fatalf("after its directory moved: watched=%v why=%q, want the watch lost", watched, why)
	}
}

// TestGoroutinesOfOneBrokerLeaseEachMessageOnce runs one broker's Receive
// from several goroutines at once over a backlog, each message acknowledged
// as it comes: every message is delivered, and none twice. The index hands a
// name to one goroutine and the rename settles the rest, in both modes.
func TestGoroutinesOfOneBrokerLeaseEachMessageOnce(t *testing.T) {
	const (
		messages   = 48
		goroutines = 6
		budget     = 30 * time.Second
	)
	type tc struct {
		name    string
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		broker := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: t.TempDir(), Policy: defaultPolicy()})
		payloads := make([][]byte, messages)
		for index := range payloads {
			payloads[index] = []byte(strconv.Itoa(index))
		}
		if _, err := broker.(corequeue.BatchPublisher).PublishMany(t.Context(), payloads); err != nil {
			t.Fatalf("PublishMany() = %v", err)
		}
		var (
			mu       sync.Mutex
			seen     = make(map[string]int)
			failures []string
			workers  sync.WaitGroup
		)
		deadline := time.Now().Add(budget)
		for range goroutines {
			workers.Go(func() {
				for time.Now().Before(deadline) {
					mu.Lock()
					done := len(seen) == messages
					mu.Unlock()
					if done {
						return
					}
					batch, err := broker.Receive(t.Context(), 1)
					if err != nil {
						mu.Lock()
						failures = append(failures, "Receive: "+err.Error())
						mu.Unlock()
						return
					}
					for _, delivery := range batch {
						mu.Lock()
						seen[delivery.Message.ID]++
						mu.Unlock()
						if ackErr := broker.Ack(t.Context(), delivery.Lease.Receipt); ackErr != nil {
							mu.Lock()
							failures = append(failures, "Ack: "+ackErr.Error())
							mu.Unlock()
						}
					}
				}
			})
		}
		workers.Wait()
		if len(failures) > 0 {
			t.Fatalf("the goroutines failed: %v", failures)
		}
		if len(seen) != messages {
			t.Fatalf("%d messages delivered within %v, want %d", len(seen), budget, messages)
		}
		for id, times := range seen {
			if times != 1 {
				t.Fatalf("message %s delivered %d times, want once", id, times)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// maxQueuedEvents reads how many events one inotify instance queues before it
// overflows.
func maxQueuedEvents(t *testing.T) int {
	t.Helper()
	raw, err := os.ReadFile("/proc/sys/fs/inotify/max_queued_events")
	if err != nil {
		t.Skipf("max_queued_events unreadable: %v", err)
	}
	limit, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if parseErr != nil || limit <= 0 {
		t.Fatalf("max_queued_events = %q, want a positive count", raw)
	}
	return limit
}
