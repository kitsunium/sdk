package queue_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// TestAStrayFileInTheQueueDirectoryIsNeverDelivered pins the claim the scan
// makes about everything that is not one of its own names.
//
// The case that matters is the FIRST one: `internal/service/vfs` publishes by
// writing a `.vfs-<hex>.tmp` beside its target and renaming, so a concurrent
// producer's half-written temporary is genuinely present in `ready/` while a
// consumer is scanning it. Delivering it would hand a consumer a truncated
// payload under a receipt naming a file about to be renamed away — the exact
// failure atomic publication exists to prevent, re-introduced one layer up.
func TestAStrayFileInTheQueueDirectoryIsNeverDelivered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	broker := newFileBroker(t, dir, defaultPolicy())
	strays := []string{
		".vfs-0123456789abcdef0123456789abcdef.tmp", // a concurrent publication, mid-flight
		"README",                  // somebody looked in the directory
		"notes.msg",               // the right suffix, the wrong shape
		"1.2.3.4.msg",             // four fields, none of them a padded instant
		"..msg",                   // pathological
		"0000000000000000001.msg", // one field where four belong
	}
	for _, stray := range strays {
		writeStray(t, filepath.Join(dir, "ready"), stray)
	}
	receiveNone(t, broker)

	//: and a real message published alongside them is still found.
	published := publish(t, broker, "genuine")
	delivery := receiveOne(t, broker)
	if delivery.Message.ID != published.ID {
		t.Fatalf("delivered %q, want the genuine message %q", delivery.Message.ID, published.ID)
	}
}

// TestAStrayFileInTheDeadLetterStoreIsNeverReturned is the same rule on the
// other scan: a burial publishes atomically too, so its temporary is present
// in `dead/` for exactly as long.
func TestAStrayFileInTheDeadLetterStoreIsNeverReturned(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	broker := newFileBroker(t, dir, defaultPolicy())
	writeStray(t, filepath.Join(dir, "dead"), ".vfs-abcdefabcdefabcdefabcdefabcdefab.tmp")
	writeStray(t, filepath.Join(dir, "dead"), "not-a-record.dead")

	if got := len(deadLetters(t, broker, 10)); got != 0 {
		t.Fatalf("DeadLetters() returned %d records, want 0 — none of the files there are ours", got)
	}
}

// TestTheDurableBrokerRefusesADirectoryAnyAccountCouldDrain pins the
// construction-time refusal. A queue directory any account may write is one
// any account may silently drain or inject into, and neither leaves a trace.
func TestTheDurableBrokerRefusesADirectoryAnyAccountCouldDrain(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "queue")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("MkdirAll() = %v, want nil", err)
	}
	//: MkdirAll applies the process umask, so the bits are set explicitly.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("Chmod() = %v, want nil", err)
	}
	_, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy()})
	if !errs.HasCode(err, svcqueue.CodeQueueDirectoryUnusable) {
		t.Fatalf("NewFile(world-writable) = %v, want CodeQueueDirectoryUnusable", err)
	}
	//: the sticky bit makes it acceptable again, because that is exactly the
	//: rule /tmp relies on: only an entry's owner may unlink it.
	if chmodErr := os.Chmod(dir, 0o777|os.ModeSticky); chmodErr != nil {
		t.Fatalf("Chmod(sticky) = %v, want nil", chmodErr)
	}
	if _, stickyErr := svcqueue.NewFile(svcqueue.FileConfig{
		Dir: dir, Policy: defaultPolicy(),
	}); stickyErr != nil {
		t.Fatalf("NewFile(sticky) = %v, want nil", stickyErr)
	}
}

// TestTheDurableBrokerRefusesAnEmptyDirectory keeps an unset Dir from
// resolving to the process's working directory, which is never what anybody
// meant.
func TestTheDurableBrokerRefusesAnEmptyDirectory(t *testing.T) {
	t.Parallel()
	_, err := svcqueue.NewFile(svcqueue.FileConfig{Policy: defaultPolicy()})
	if !errs.HasCode(err, svcqueue.CodeQueueDirectoryUnusable) {
		t.Fatalf("NewFile(no Dir) = %v, want CodeQueueDirectoryUnusable", err)
	}
}

// TestTwoBrokersOverOneDirectoryAreOneQueue is the domain's inter-process
// claim, exercised in one process because that is the half a unit test can
// reach: the cross-process half is
// TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack.
func TestTwoBrokersOverOneDirectoryAreOneQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	producer := newFileBrokerWithClock(t, dir, clk)
	consumer := newFileBrokerWithClock(t, dir, clk)

	published := publish(t, producer, "crosses")
	delivery := receiveOne(t, consumer)
	if delivery.Message.ID != published.ID {
		t.Fatalf("the second broker leased %q, want %q", delivery.Message.ID, published.ID)
	}
	//: and the FIRST broker now sees nothing, because the lease is a fact on
	//: the disk rather than in either broker's heap.
	receiveNone(t, producer)

	//: an acknowledgement from the consumer is honoured by the producer's view.
	if err := consumer.Ack(t.Context(), delivery.Lease.Receipt); err != nil {
		t.Fatalf("Ack() = %v, want nil", err)
	}
	clk.Advance(testVisibility * 2)
	receiveNone(t, producer)
}

// writeStray drops a file that is not one of the broker's own names into one
// of its state directories.
func writeStray(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not a message"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) = %v, want nil", name, err)
	}
}

// newFileBroker builds a durable broker on a ManualClock at the epoch.
func newFileBroker(t *testing.T, dir string, policy corequeue.PolicyValue) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{
		Dir: dir, Policy: policy, Clock: clock.NewManualClock(epoch),
	})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	return broker
}

// newFileBrokerWithClock builds a durable broker sharing a caller's clock, so
// two of them can be advanced together.
func newFileBrokerWithClock(t *testing.T, dir string, clk clock.Clock) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{
		Dir: dir, Policy: defaultPolicy(), Clock: clk,
	})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	return broker
}
