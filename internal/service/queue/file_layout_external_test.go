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

// TestTheDurableBrokerRefusesAStateDirectoryItCannotTrust applies the root's
// refusal one level down, where the messages actually live. Only the queue
// directory used to be checked, so under a root the rule accepts — a group
// share, or a sticky /tmp-like directory — an `inflight/` or a `ready/` that
// somebody else had already put there was trusted whatever it was: another
// account could pre-create a world-writable `ready/` and plant or unlink
// messages in it, which is the silent injection and the silent drain the root
// check exists to refuse.
//
// The symlink case is the one no mode check can see. It points at a private,
// perfectly acceptable directory, so the only thing wrong with it is that it
// is a link — and a state that is a link keeps the queue's messages wherever
// the link's author chose, or, pointed at a sibling state, makes two states
// one directory.
//
// Seen failing: with makeStates restored to MkdirAll-and-trust, the
// world-writable and the symlink cases printed
//
//	NewFile() = <nil>, want CodeQueueDirectoryUnusable
//
// and the regular-file case, which MkdirAll reports as ENOTDIR, printed
//
//	NewFile() = [0.3.53.1 QUEUE_BACKEND_FAILED] The queue's storage refused an
//	operation, want CodeQueueDirectoryUnusable
func TestTheDurableBrokerRefusesAStateDirectoryItCannotTrust(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state string
		why   string
		plant func(t *testing.T, statePath string)
	}{
		{"a world-writable ready/", "ready", "world-writable", func(t *testing.T, statePath string) {
			t.Helper()
			mkdirMode(t, statePath, 0o777)
		}},
		{
			"an inflight/ that is a symlink to a private directory", "inflight", "symlink",
			func(t *testing.T, statePath string) {
				t.Helper()
				elsewhere := filepath.Join(t.TempDir(), "elsewhere")
				mkdirMode(t, elsewhere, 0o700)
				if err := os.Symlink(elsewhere, statePath); err != nil {
					t.Fatalf("Symlink() = %v, want nil", err)
				}
			},
		},
		{"a dead/ that is a regular file", "dead", "not-a-directory", func(t *testing.T, statePath string) {
			t.Helper()
			if err := os.WriteFile(statePath, nil, 0o600); err != nil {
				t.Fatalf("WriteFile() = %v, want nil", err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "queue")
			mkdirMode(t, root, 0o700)
			tc.plant(t, filepath.Join(root, tc.state))
			_, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: root, Policy: defaultPolicy()})
			if !errs.HasCode(err, svcqueue.CodeQueueDirectoryUnusable) {
				t.Fatalf("NewFile() = %v, want CodeQueueDirectoryUnusable", err)
			}
			//: the refusal names the state and the reason, so an operator can
			//: find the directory without reading this package.
			if got := fieldValue(err, "state"); got != tc.state {
				t.Errorf("state field = %q, want %q", got, tc.state)
			}
			if got := fieldValue(err, "why"); got != tc.why {
				t.Errorf("why field = %q, want %q", got, tc.why)
			}
		})
	}
}

// TestTheDurableBrokerCreatesItsStatesOwnerOnlyAndReopensThem is the other
// side of the refusal above: what the broker makes itself is exactly what the
// check accepts, including on the second construction over the same
// directory — the path every restart and every second process takes.
func TestTheDurableBrokerCreatesItsStatesOwnerOnlyAndReopensThem(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "queue")
	for attempt := range 2 {
		if _, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: root, Policy: defaultPolicy()}); err != nil {
			t.Fatalf("NewFile() on construction %d = %v, want nil", attempt+1, err)
		}
	}
	for _, state := range []string{"ready", "inflight", "dead"} {
		info, err := os.Lstat(filepath.Join(root, state))
		if err != nil {
			t.Fatalf("Lstat(%s) = %v, want nil", state, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is %v, want a real directory", state, info.Mode())
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Errorf("%s created with %v, want -rwx------", state, got)
		}
	}
}

// TestTheDurableBrokerAcceptsStatesSharedThroughAGroup keeps the deliberate
// arrangement checkQueueDir names — two service accounts sharing a queue
// through a common group — working once the states are checked too.
func TestTheDurableBrokerAcceptsStatesSharedThroughAGroup(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "queue")
	mkdirMode(t, root, 0o770)
	for _, state := range []string{"ready", "inflight", "dead"} {
		mkdirMode(t, filepath.Join(root, state), 0o770)
	}
	if _, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: root, Policy: defaultPolicy()}); err != nil {
		t.Fatalf("NewFile(group-writable root and states) = %v, want nil", err)
	}
}

// mkdirMode creates dir with exactly mode: Mkdir applies the process umask, so
// the bits are set explicitly afterwards.
func mkdirMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(dir, mode); err != nil {
		t.Fatalf("Mkdir(%s) = %v, want nil", dir, err)
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatalf("Chmod(%s) = %v, want nil", dir, err)
	}
}

// fieldValue returns the value of the named field on err, or "" when it
// carries none.
func fieldValue(err error, key string) string {
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
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
