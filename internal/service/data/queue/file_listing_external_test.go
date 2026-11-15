package queue_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The file broker reuses a state directory's last listing while the
// directory's mtime has not moved and was already older than the listing by
// a margin — git's "racily clean" rule (issue #811). These cases hold the
// rule where it matters: a change the mtime does not show. A filesystem whose
// timestamps are coarser than the interval between two changes leaves exactly
// that — the second change in the listing's tick stamps the mtime the listing
// recorded —, and os.Chtimes puts the recorded mtime back to play that
// filesystem on any other.

// publisherDirEnv carries the queue directory to the child process that
// publishes one message and exits. Its presence turns [TestThePublisherProcess]
// from a skip into a run, as the victim consumer's does (CLAUDE.md rule 12).
const publisherDirEnv string = "KTNQ_PUBLISHER_QUEUE_DIR"

// publisherPayload is what the child process publishes.
const publisherPayload string = "from-another-process"

// publisherBudget bounds the child process's life, whatever became of the
// test that started it.
const publisherBudget time.Duration = 30 * time.Second

// brokerIn builds a durable broker over cfg, its directories unwatched when
// unwatch is set: read again under the listings' rule, the mode every
// platform but Linux runs, forced here so Linux runs it too.
func brokerIn(t *testing.T, unwatch bool, cfg svcqueue.FileConfig) corequeue.Broker {
	t.Helper()
	broker := requireFileBroker(t, cfg)
	if unwatch {
		svcqueue.UnwatchForTest(broker)
	}
	return broker
}

// TestThePublisherProcess is the child [TestAPublicationByAnotherProcessInTheTickOfAListingIsSeen]
// starts: it publishes one message into the directory it is given, on the
// real clock, and exits. It starts nothing.
func TestThePublisherProcess(t *testing.T) {
	dir := os.Getenv(publisherDirEnv)
	if dir == "" {
		t.Skip("not the publisher: " + publisherDirEnv + " is unset")
	}
	publish(t, requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()}), publisherPayload)
}

// publishFromAnotherProcess runs [TestThePublisherProcess] in a child of this
// test binary over dir, within publisherBudget. The child's environment is
// this one with the role's variable REMOVED, then set: a value inherited from
// a run that was itself a child never names a second directory.
func publishFromAnotherProcess(t *testing.T, dir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), publisherBudget)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestThePublisherProcess$") //nolint:gosec
	child.Env = append(slices.DeleteFunc(os.Environ(), func(entry string) bool {
		return strings.HasPrefix(entry, publisherDirEnv+"=")
	}), publisherDirEnv+"="+dir)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("the publisher process = %v\n%s", err, out)
	}
}

// TestAPublicationByAnotherProcessInTheTickOfAListingIsSeen is the decision's
// proof: a consumer lists an empty queue, another PROCESS publishes into it
// in the same tick — the mtime the listing recorded does not move — and the
// consumer's next Receive delivers the message: watched, because the kernel
// queued the rename before it returned; unwatched, because a listing whose
// directory changed within racyMargin of it is never reused.
func TestAPublicationByAnotherProcessInTheTickOfAListingIsSeen(t *testing.T) {
	type tc struct {
		name string
		// unwatch reads the directories under the listings' rule: watched,
		// the kernel's change queue keeps the index where it has one.
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		consumer := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		receiveNone(t, consumer)
		listed := stateMtime(t, dir, "ready")

		publishFromAnotherProcess(t, dir)
		pinMtime(t, dir, "ready", listed)

		got := receiveOne(t, consumer)
		if string(got.Message.Payload) != publisherPayload {
			t.Fatalf("Receive delivered %q, want the other process's %q", got.Message.Payload, publisherPayload)
		}
	}
	//: each mode.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAPublicationByAnotherBrokerInTheTickOfAListingIsSeen is the same proof
// with a second broker over the directory — its own os.Root, its own
// publisher — and a queue that already held a message, so the listing that
// misses the publication is not an empty one.
func TestAPublicationByAnotherBrokerInTheTickOfAListingIsSeen(t *testing.T) {
	type tc struct {
		name string
		// unwatch reads the directories under the listings' rule: watched,
		// the kernel's change queue keeps the index where it has one.
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		consumer := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		producer := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		first := publish(t, producer, "first")
		if got := receiveOne(t, consumer); got.Message.ID != first.ID {
			t.Fatalf("Receive delivered %q, want %q", got.Message.ID, first.ID)
		}
		listed := stateMtime(t, dir, "ready")

		second := publish(t, producer, "second")
		pinMtime(t, dir, "ready", listed)

		if got := receiveOne(t, consumer); got.Message.ID != second.ID {
			t.Fatalf("Receive delivered %q, want the publication made in the listing's tick, %q", got.Message.ID, second.ID)
		}
	}
	//: each mode.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestACoarseMtimeWithinTheMarginOfItsListingIsNeverTrusted plays a filesystem
// that stamps in whole seconds — two, as FAT does — at its worst: the
// directory's mtime is a whole number of seconds and the listing begins just
// inside racyMargin after it, so a publication later in the same coarse tick
// stamps the same mtime. The listing is not reused and the publication is
// seen.
func TestACoarseMtimeWithinTheMarginOfItsListingIsNeverTrusted(t *testing.T) {
	type tc struct {
		name string
		// unwatch reads the directories under the listings' rule: watched,
		// the kernel's change queue keeps the index where it has one.
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		consumer := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		producer := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		coarse := time.Now().Add(-time.Hour).Truncate(2 * time.Second)
		pinMtime(t, dir, "ready", coarse)
		svcqueue.SetListingWallForTest(consumer, func() time.Time {
			return coarse.Add(svcqueue.RacyMarginForTest - time.Nanosecond)
		})
		receiveNone(t, consumer)

		published := publish(t, producer, "same-coarse-tick")
		pinMtime(t, dir, "ready", coarse)

		if got := receiveOne(t, consumer); got.Message.ID != published.ID {
			t.Fatalf("Receive delivered %q, want %q", got.Message.ID, published.ID)
		}
	}
	//: each mode.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAListingAnMtimeCannotInvalidateIsReadAgainPastItsMaximumAge is the one
// case the margin cannot see, shown rather than discovered: the directory's
// mtime was already old at the listing and is old again after a publication —
// a tool that restores a directory's mtime, a wall clock stepped back across
// the listing. Unwatched, the listing is reused, so the publication is not
// seen at once; it is seen once the listing is listingMaxAge old, which
// bounds the delay whatever the mtime says. Watched, the kernel named the
// rename, and the next Receive delivers it: the mtime is never read.
func TestAListingAnMtimeCannotInvalidateIsReadAgainPastItsMaximumAge(t *testing.T) {
	type tc struct {
		name string
		// unwatch reads the directories under the listings' rule: watched,
		// the kernel's change queue keeps the index where it has one.
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		consumer := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		producer := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
		var ahead atomic.Int64
		svcqueue.SetListingWallForTest(consumer, func() time.Time {
			return time.Now().Add(time.Duration(ahead.Load()))
		})
		old := time.Now().Add(-time.Hour)
		pinMtime(t, dir, "ready", old)
		receiveNone(t, consumer)

		published := publish(t, producer, "behind-a-forged-mtime")
		pinMtime(t, dir, "ready", old)
		if watched, _ := svcqueue.WatchStateForTest(consumer); watched {
			if got := receiveOne(t, consumer); got.Message.ID != published.ID {
				t.Fatalf("a watched Receive delivered %q, want %q at once", got.Message.ID, published.ID)
			}
			return
		}
		receiveNone(t, consumer)

		ahead.Store(int64(svcqueue.ListingMaxAgeForTest))
		if got := receiveOne(t, consumer); got.Message.ID != published.ID {
			t.Fatalf("Receive past the listing's maximum age delivered %q, want %q", got.Message.ID, published.ID)
		}
	}
	//: each mode.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// TestAnIdlePollOfAQuietQueueReadsNoDirectory is what the cache is for: on a
// queue nobody has touched for longer than the margin, a Receive and a Stats
// cost a stat of each state directory — watched, a read of the empty change
// queue — and no read of one. The directories are made unreadable after the
// first look — a read would fail with EACCES —, and every later call still
// answers, from the listings and the index, what the queue holds.
func TestAnIdlePollOfAQuietQueueReadsNoDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the superuser reads a directory whatever its mode, so an unreadable one proves nothing")
	}
	type tc struct {
		name string
		// unwatch reads the directories under the listings' rule: watched,
		// the kernel's change queue keeps the index where it has one.
		unwatch bool
	}
	tests := []tc{{name: "watched"}, {name: "unwatched", unwatch: true}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dir := t.TempDir()
		clk := clock.NewManualClock(epoch)
		broker := brokerIn(t, c.unwatch, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
		later := delayedPublisher(t, broker)
		for range 3 {
			if _, err := later.PublishAt(t.Context(), []byte("later"), epoch.Add(time.Hour)); err != nil {
				t.Fatalf("PublishAt() = %v", err)
			}
		}
		quiet := time.Now().Add(-time.Hour)
		for _, state := range []string{"ready", "inflight", "dead"} {
			pinMtime(t, dir, state, quiet)
		}
		receiveNone(t, broker)
		counter := broker.(corequeue.Counter)
		if _, err := counter.Stats(t.Context()); err != nil {
			t.Fatalf("Stats() = %v", err)
		}

		for _, state := range []string{"ready", "inflight", "dead"} {
			unreadable(t, dir, state)
		}
		for range 5 {
			receiveNone(t, broker)
		}
		stats, err := counter.Stats(t.Context())
		if err != nil {
			t.Fatalf("Stats() over unreadable, quiet directories = %v, want the listings' answer", err)
		}
		if stats.Queued != 3 || stats.Leased != 0 || stats.Dead != 0 || !stats.NextDueAt.Equal(epoch.Add(time.Hour)) {
			t.Fatalf("Stats() = %+v, want 3 queued, due at %v", stats, epoch.Add(time.Hour))
		}
	}
	//: each mode.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// stateMtime returns the mtime of one state directory of the queue at dir.
func stateMtime(t *testing.T, dir, state string) time.Time {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, state))
	if err != nil {
		t.Fatalf("Stat(%s) = %v", state, err)
	}
	return info.ModTime()
}

// pinMtime sets one state directory's mtime to at — what a filesystem whose
// timestamps are coarser than the interval between two changes leaves.
func pinMtime(t *testing.T, dir, state string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(dir, state), at, at); err != nil {
		t.Fatalf("Chtimes(%s) = %v", state, err)
	}
}

// unreadable takes the read permission off one state directory — a mode
// change moves its ctime, never its mtime — and gives it back when the case ends — before the temporary
// directory is removed, which needs to list it.
func unreadable(t *testing.T, dir, state string) {
	t.Helper()
	path := filepath.Join(dir, state)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) = %v", state, err)
	}
	if chmodErr := os.Chmod(path, 0o300); chmodErr != nil {
		t.Fatalf("Chmod(%s) = %v", state, chmodErr)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chmod(path, info.Mode().Perm()); restoreErr != nil {
			t.Errorf("Chmod(%s) back = %v", state, restoreErr)
		}
	})
}
