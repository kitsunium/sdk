package queue_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
)

// victimDirEnv carries the queue directory to the child process. Its presence
// is also what turns [TestTheVictimConsumer] from a skip into a run, so the
// helper is DISCOVERED by `go test ./...` like any other test and needs no
// build tag, no `manual` target and no compensating lane (CLAUDE.md rule 12).
const victimDirEnv string = "KTNQ_VICTIM_QUEUE_DIR"

// leasedMarker is what the child prints once it is holding a lease. The
// parent waits for this exact line rather than for a duration, so the kill
// lands at a known point in the child's life instead of at a hopeful one.
const leasedMarker string = "KTNQ-LEASED "

// deathVisibility is the lease lifetime for the death test.
//
// It is a REAL duration and this test really waits it out. That is
// deliberate, and it is the one place in this package that touches the wall
// clock: the lease deadline is a wall-clock instant written into a filename
// precisely so that ANOTHER PROCESS can read it, and another process does not
// share this one's ManualClock. A fake clock here would prove that the parent
// agrees with itself, which is exactly the thing this test exists not to
// settle.
const deathVisibility time.Duration = 400 * time.Millisecond

// deathBudget bounds how long the parent waits for the redelivery before
// calling the test failed.
const deathBudget time.Duration = 30 * time.Second

// TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack is the domain's
// central claim, and it is proved the only way it can be proved: by killing a
// real consumer, in a real other process, with a signal it cannot catch.
//
// # What this test proves that a Nack test cannot
//
// A test that calls Nack proves that Nack works. It says nothing about the
// case the whole domain exists for, because a consumer that nacks is a
// consumer that is still running — it got to its error path, it had a stack,
// it had a deferred function. This test removes all three:
//
//   - The child is a SEPARATE PROCESS, so nothing in the parent's heap can be
//     what makes the message reappear. Every fact the recovery needs — the
//     lease, its deadline, the delivery count — has to have been on the disk
//     already, because the process that knew them is gone.
//   - It is killed with SIGKILL, which cannot be caught, blocked or handled.
//     No deferred function runs, no signal handler runs, no buffered write is
//     flushed, and no "graceful shutdown" path executes. The child does not
//     get to participate in its own cleanup, which is the entire difference
//     between this and a cancelled context.
//   - The kill lands while the lease is HELD and the message is not visible
//     to anybody — asserted, not assumed, by the parent's own Receive
//     returning nothing first. That assertion is also the inter-process
//     claim: the parent is a different process obeying a lease it never took.
//
// What comes back afterwards is therefore the queue's doing and not the
// consumer's: the same message, with Deliveries incremented to 2 by a count
// that survived in a directory entry.
func TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack(t *testing.T) {
	//: not parallel: it spawns a process and waits on the real clock.
	dir := t.TempDir()
	broker := deathBroker(t, dir)
	published := publish(t, broker, "survive-me")

	victim, stdout := startVictim(t, dir)
	leasedID := awaitLease(t, stdout)
	if leasedID != published.ID {
		t.Fatalf("the child leased %q, want the published %q", leasedID, published.ID)
	}

	//: the inter-process claim, asserted before the kill: this parent is a
	//: DIFFERENT process and it can see nothing, because a lease taken in the
	//: child is a fact on the disk that binds everybody.
	receiveNone(t, broker)

	//: SIGKILL. Not Interrupt, not a cancelled context, not a closed pipe —
	//: the child gets no opportunity to release, nack, flush or log.
	if killErr := victim.Process.Kill(); killErr != nil {
		t.Fatalf("Kill() = %v, want nil", killErr)
	}
	//: Wait ALWAYS reports a failure here, because the child was killed by a
	//: signal — that is the point of the test, so the error is inspected and
	//: discarded by name rather than by an underscore.
	if waitErr := victim.Wait(); waitErr == nil {
		t.Fatal("the victim exited cleanly; a SIGKILLed process must not")
	}

	//: nothing has changed yet: the dead consumer's lease is still honoured
	//: until its deadline, which is the whole reason a redelivery is bounded
	//: rather than immediate.
	receiveNone(t, broker)

	redelivered := awaitRedelivery(t, broker)
	if redelivered.Message.ID != published.ID {
		t.Fatalf("redelivered %q, want %q", redelivered.Message.ID, published.ID)
	}
	if string(redelivered.Message.Payload) != "survive-me" {
		t.Fatalf("redelivered payload %q, want %q", redelivered.Message.Payload, "survive-me")
	}
	if redelivered.Deliveries != 2 {
		t.Fatalf("Deliveries = %d, want 2 — the count must survive in the queue, not in the "+
			"process that died holding it", redelivered.Deliveries)
	}
}

// TestTheVictimConsumer is the child. It leases one message, announces the
// fact, and then blocks until it is killed.
//
// It skips unless the parent named a queue directory, so `go test ./...`
// discovers it, runs it, and it costs nothing.
func TestTheVictimConsumer(t *testing.T) {
	dir := os.Getenv(victimDirEnv)
	if dir == "" {
		t.Skip("not the victim: " + victimDirEnv + " is unset")
	}
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: deathPolicy()})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	batch, receiveErr := broker.Receive(t.Context(), 1)
	if receiveErr != nil || len(batch) != 1 {
		t.Fatalf("Receive(1) = %v, %v; want one delivery", len(batch), receiveErr)
	}
	//: os.Stdout is unbuffered, so the parent sees this line as soon as the
	//: write returns — which matters, because the parent kills on it.
	fmt.Fprintln(os.Stdout, leasedMarker+batch[0].Message.ID) //nolint:forbidigo

	//: block in a SYSCALL rather than on a channel: a channel with nothing to
	//: send it would be a deadlock the Go runtime detects and panics on, and a
	//: panicking child is a child that ran a deferred function. Reading a pipe
	//: the parent holds open never returns and never runs anything.
	//
	//: this Copy is not expected to return at all — the process is supposed to
	//: die inside it — so its outcome is reported rather than discarded, and
	//: reaching either line below is itself the failure.
	_, copyErr := io.Copy(io.Discard, os.Stdin)
	t.Fatalf("the victim was supposed to be killed while holding its lease, and was not (%v)", copyErr)
}

// deathPolicy is the policy both processes construct their broker with. They
// have to agree: the lease deadline the child writes is the one the parent
// reads.
func deathPolicy() corequeue.PolicyValue {
	return corequeue.PolicyValue{VisibilityTimeout: deathVisibility, MaxDeliveries: 5}
}

// deathBroker builds the parent's broker on the REAL clock.
func deathBroker(t *testing.T, dir string) corequeue.Broker {
	t.Helper()
	broker, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: deathPolicy()})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	return broker
}

// startVictim launches this test binary again, in a child process, running
// only [TestTheVictimConsumer].
//
// IFACE-OPAQUE: the returned reader is a *bufio.Scanner over the child's
// stdout pipe, kept behind [lineReader] because the parent's only use for it
// is to pull lines until the lease marker arrives.
func startVictim(t *testing.T, dir string) (victim *exec.Cmd, stdout lineReader) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestTheVictimConsumer$", "-test.v") //nolint:gosec
	child.Env = append(os.Environ(), victimDirEnv+"="+dir)
	//: the parent holds the write end open for the child's life, which is
	//: what makes the child's blocking read never return.
	if _, pipeErr := child.StdinPipe(); pipeErr != nil {
		t.Fatalf("StdinPipe() = %v, want nil", pipeErr)
	}
	pipe, outErr := child.StdoutPipe()
	if outErr != nil {
		t.Fatalf("StdoutPipe() = %v, want nil", outErr)
	}
	if startErr := child.Start(); startErr != nil {
		t.Fatalf("Start() = %v, want nil", startErr)
	}
	//: a child that outlives a failing parent would hold its lease forever.
	//: A kill that fails here means the child is already gone, which is the
	//: ordinary case once the test has done its own killing.
	t.Cleanup(func() {
		if killErr := child.Process.Kill(); killErr != nil {
			t.Logf("the victim was already gone at cleanup: %v", killErr)
		}
	})
	return child, bufio.NewScanner(pipe)
}

// lineReader is the half of bufio.Scanner this test uses: pull one line at a
// time until there are none.
//
// IFACE-OPAQUE: the concrete type is *bufio.Scanner and stays unexported
// behind [startVictim], because the only thing the parent does with the
// child's output is read lines out of it until the lease marker arrives.
type lineReader interface {
	Scan() bool
	Text() string
}

// awaitLease reads the child's output until it announces the lease.
func awaitLease(t *testing.T, stdout lineReader) string {
	t.Helper()
	//: until the marker arrives or the child's stdout closes.
	for stdout.Scan() {
		line := stdout.Text()
		if after, found := strings.CutPrefix(line, leasedMarker); found {
			return after
		}
	}
	t.Fatal("the victim exited without ever announcing a lease")
	return ""
}

// awaitRedelivery polls until the lapsed lease is recovered, or the budget
// runs out.
//
// It polls rather than sleeping once for exactly the visibility timeout,
// because the point being proved is that the message COMES BACK, not that it
// comes back at a particular millisecond — and a single sleep would make this
// test fail on a loaded machine for a reason that is not the domain's.
func awaitRedelivery(t *testing.T, broker corequeue.Broker) corequeue.DeliveryValue {
	t.Helper()
	deadline := time.Now().Add(deathBudget)
	for time.Now().Before(deadline) {
		batch, err := broker.Receive(t.Context(), 1)
		if err != nil {
			t.Fatalf("Receive() = %v, want nil", err)
		}
		if len(batch) == 1 {
			return batch[0]
		}
		time.Sleep(deathVisibility / 8)
	}
	t.Fatalf("the killed consumer's message never came back within %v", deathBudget)
	return corequeue.DeliveryValue{}
}
