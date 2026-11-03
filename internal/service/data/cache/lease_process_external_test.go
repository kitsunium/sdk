package cache_test

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corelock "github.com/kitsunium/sdk/internal/core/app/lock"
	corecache "github.com/kitsunium/sdk/internal/core/data/cache"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svclock "github.com/kitsunium/sdk/internal/service/app/lock"
	svccache "github.com/kitsunium/sdk/internal/service/data/cache"
)

// leaseDirEnv carries the shared directory to the child process. Its presence
// is also what turns [TestTheLeaseHolderProcess] from a skip into a run, so
// the helper is discovered by `go test ./...` like any other test and needs no
// build tag, no `manual` target and no compensating lane (the queue's death
// test does the same).
const leaseDirEnv string = "KTNC_LEASE_DIR"

// leaseHeldMarker is what the child prints once it holds the lease and is
// inside its fill; leaseDoneMarker, followed by the value, once its
// LoadLeased has returned — the lease released.
const (
	leaseHeldMarker string = "KTNC-LEASE-HELD"
	leaseDoneMarker string = "KTNC-LEASE-DONE "
)

// leaseKey is the one key both processes ask for.
const leaseKey string = "hot"

// processPoll is the waiting process's step on its manual clock.
const processPoll time.Duration = 50 * time.Millisecond

// processBudget bounds every real-time wait on the other process. It is a
// REAL duration because another process does not share this one's manual
// clock; it is a ceiling for a broken run, never a wait a passing run makes.
const processBudget time.Duration = 30 * time.Second

// TestTwoProcessesOverOneFileLockerFillOnce is #830's first acceptance, proved
// across a real process boundary: a child process takes the key's lease from
// the lock domain's file locker and fills; this process, over the same lock
// directory and the same shared tier, fills nothing while the child holds the
// lease, and is answered by the child's value within one poll step of its
// clock once the child has stored it. A file both processes append to counts
// the fills: one.
func TestTwoProcessesOverOneFileLockerFillOnce(t *testing.T) {
	//: not parallel: it spawns a process and waits on it in real time.
	dir := t.TempDir()
	child, lines, stdin := startLeaseHolder(t, dir)
	awaitLine(t, lines, leaseHeldMarker)

	manual := clock.NewManualClock(time.Unix(0, 0))
	done := loadLeasedAsync(t.Context(), leaserOf(t, processChain(t, dir, manual)), leaseKey, recordingFill(t, dir, "waiter"), fileLockerIn(t, dir))
	//: the child holds the lease: step after step, nothing fills here.
	for range 3 {
		manual.BlockUntil(1)
		manual.Advance(processPoll)
	}
	manual.BlockUntil(1)
	if got := readFills(t, dir); got != "holder\n" {
		t.Fatalf("fills while the child held the lease = %q, want only the child's", got)
	}

	//: let the child's fill return; it stores, releases and says so.
	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatalf("writing to the child = %v, want nil", err)
	}
	awaitLine(t, lines, leaseDoneMarker+"from-holder")
	if err := child.Wait(); err != nil {
		t.Fatalf("the child exited with %v, want a clean exit", err)
	}

	//: ONE step later, this process reads the child's value.
	manual.Advance(processPoll)
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.value != "from-holder" {
		t.Fatalf("LoadLeased = %q, %v; want the child's from-holder, nil", outcome.value, outcome.err)
	}
	if got := readFills(t, dir); got != "holder\n" {
		t.Fatalf("fills = %q, want exactly one, the child's", got)
	}
}

// TestAKilledLeaseHolderLetsTheWaiterFillOnce is #830's second acceptance: a
// child SIGKILLed inside its fill — before any Set, with no deferred function
// run — loses the lease with its life (flock(2) dies with the process), and
// this process, which filled nothing while the child lived, then fills once.
func TestAKilledLeaseHolderLetsTheWaiterFillOnce(t *testing.T) {
	//: not parallel: it spawns a process and kills it.
	dir := t.TempDir()
	child, lines, _ := startLeaseHolder(t, dir)
	awaitLine(t, lines, leaseHeldMarker)

	manual := clock.NewManualClock(time.Unix(0, 0))
	done := loadLeasedAsync(t.Context(), leaserOf(t, processChain(t, dir, manual)), leaseKey, recordingFill(t, dir, "waiter"), fileLockerIn(t, dir))
	for range 3 {
		manual.BlockUntil(1)
		manual.Advance(processPoll)
	}
	manual.BlockUntil(1)
	if got := readFills(t, dir); got != "holder\n" {
		t.Fatalf("fills while the child held the lease = %q, want only the child's", got)
	}

	//: SIGKILL: the child releases nothing and stores nothing.
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("Kill() = %v, want nil", err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("the killed child exited cleanly; a SIGKILLed process must not")
	}

	manual.Advance(processPoll)
	outcome := awaitOutcome(t, done)
	if outcome.err != nil || outcome.value != "from-waiter" {
		t.Fatalf("LoadLeased = %q, %v; want this process's own from-waiter, nil", outcome.value, outcome.err)
	}
	if got := readFills(t, dir); got != "holder\nwaiter\n" {
		t.Fatalf("fills = %q, want the dead child's then this process's, one each", got)
	}
}

// TestTheLeaseHolderProcess is the child. It takes the lease, records its
// fill, announces it, and waits for the parent's word — or to be killed.
//
// It skips unless the parent named a directory, so `go test ./...` discovers
// it, runs it, and it costs nothing.
func TestTheLeaseHolderProcess(t *testing.T) {
	dir := os.Getenv(leaseDirEnv)
	if dir == "" {
		t.Skip("not the lease holder: " + leaseDirEnv + " is unset")
	}
	stdin := bufio.NewReader(os.Stdin)
	fill := func(context.Context) (corecache.EntryValue[string], error) {
		if err := appendFill(dir, "holder"); err != nil {
			return corecache.EntryValue[string]{}, err
		}
		//: os.Stdout is unbuffered: the parent sees the line when the write
		//: returns, and it acts on it.
		fmt.Fprintln(os.Stdout, leaseHeldMarker) //nolint:forbidigo
		//: a killed child never gets past this read.
		if _, err := stdin.ReadString('\n'); err != nil {
			return corecache.EntryValue[string]{}, err
		}
		return corecache.EntryValue[string]{Value: "from-holder", TTL: corecache.NoExpiry}, nil
	}
	value, err := leaserOf(t, processChain(t, dir, clock.System)).LoadLeased(t.Context(), leaseKey, fill, fileLockerIn(t, dir))
	if err != nil {
		t.Fatalf("LoadLeased = %v, want nil", err)
	}
	fmt.Fprintln(os.Stdout, leaseDoneMarker+value) //nolint:forbidigo
}

// startLeaseHolder launches this test binary again, running only
// [TestTheLeaseHolderProcess] over dir.
//
// IFACE-OPAQUE: the child's stdout comes back as a channel of lines, read by a
// goroutine of its own so that every wait on it can be bounded.
func startLeaseHolder(t *testing.T, dir string) (child *exec.Cmd, lines <-chan string, stdin io.Writer) {
	t.Helper()
	child = exec.Command(os.Args[0], "-test.run=^TestTheLeaseHolderProcess$", "-test.v") //nolint:gosec
	child.Env = append(os.Environ(), leaseDirEnv+"="+dir)
	in, inErr := child.StdinPipe()
	if inErr != nil {
		t.Fatalf("StdinPipe() = %v, want nil", inErr)
	}
	out, outErr := child.StdoutPipe()
	if outErr != nil {
		t.Fatalf("StdoutPipe() = %v, want nil", outErr)
	}
	if startErr := child.Start(); startErr != nil {
		t.Fatalf("Start() = %v, want nil", startErr)
	}
	//: a child that outlives a failing parent would hold its lease forever.
	t.Cleanup(func() {
		if killErr := child.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			t.Logf("the lease holder was already gone at cleanup: %v", killErr)
		}
	})
	sent := make(chan string, 64)
	go func() {
		defer close(sent)
		scanner := bufio.NewScanner(out)
		for scanner.Scan() {
			sent <- scanner.Text()
		}
	}()
	return child, sent, in
}

// awaitLine reads the child's lines until one starts with want.
func awaitLine(t *testing.T, lines <-chan string, want string) {
	t.Helper()
	deadline := time.After(processBudget)
	for {
		select {
		case line, open := <-lines:
			if !open {
				t.Fatalf("the child's output ended before %q", want)
			}
			if strings.HasPrefix(line, want) {
				return
			}
		case <-deadline:
			t.Fatalf("no %q from the child within %v", want, processBudget)
		}
	}
}

// awaitOutcome waits for a LoadLeased running on another goroutine.
func awaitOutcome(t *testing.T, done <-chan leaseOutcome) leaseOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(processBudget):
		t.Fatalf("LoadLeased did not return within %v", processBudget)
		return leaseOutcome{}
	}
}

// processChain is the store both processes build: a memory tier of their own
// in front of the directory tier they share.
func processChain(t *testing.T, dir string, waiter clock.Timed) corecache.Store[string] {
	t.Helper()
	near := newStore(t, svccache.MemoryConfig{MaxEntries: 8})
	chain, err := svccache.NewChain(svccache.ChainConfig{Clock: waiter, LeasePoll: processPoll}, near, dirTier{dir: filepath.Join(dir, "values")})
	if err != nil {
		t.Fatalf("NewChain() = %v, want nil", err)
	}
	return chain
}

// fileLockerIn is the lock domain's file locker over dir's lock directory.
func fileLockerIn(t *testing.T, dir string) corelock.Locker {
	t.Helper()
	locker, err := svclock.NewFileLocker(svclock.FileConfig{Dir: filepath.Join(dir, "locks")})
	if err != nil {
		t.Fatalf("NewFileLocker() = %v, want nil", err)
	}
	return locker
}

// recordingFill is this process's fill: it records itself, then answers.
func recordingFill(t *testing.T, dir, who string) corecache.Fill[string] {
	t.Helper()
	return func(context.Context) (corecache.EntryValue[string], error) {
		if err := appendFill(dir, who); err != nil {
			return corecache.EntryValue[string]{}, err
		}
		return corecache.EntryValue[string]{Value: "from-" + who, TTL: corecache.NoExpiry}, nil
	}
}

// appendFill records one fill by who in the file both processes share.
func appendFill(dir, who string) error {
	file, err := os.OpenFile(filepath.Join(dir, "fills"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, writeErr := file.WriteString(who + "\n"); writeErr != nil {
		_ = file.Close()
		return writeErr
	}
	return file.Close()
}

// readFills returns every fill recorded so far, one per line.
func readFills(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "fills"))
	if err != nil {
		t.Fatalf("reading the fills = %v, want nil", err)
	}
	return string(data)
}

// dirTier is a cache tier two processes share: one file per key in a
// directory, written to a temporary then renamed so a reader sees a whole
// value or none. It keeps no deadline and no tag — the test needs neither —
// and answers the whole tier contract.
type dirTier struct{ dir string }

// pathOf names key's file: hex, so any key is one plain name.
func (d dirTier) pathOf(key string) string {
	return filepath.Join(d.dir, hex.EncodeToString([]byte(key)))
}

// Fetch reads key's file.
func (d dirTier) Fetch(ctx context.Context, key string) (value string, found bool, err error) {
	entry, found, err := d.FetchEntry(ctx, key)
	return entry.Value, found, err
}

// FetchEntry reads key's file; an absent file is a miss.
func (d dirTier) FetchEntry(_ context.Context, key string) (entry corecache.EntryValue[string], found bool, err error) {
	data, readErr := os.ReadFile(d.pathOf(key))
	if errors.Is(readErr, fs.ErrNotExist) {
		return corecache.EntryValue[string]{}, false, nil
	}
	if readErr != nil {
		return corecache.EntryValue[string]{}, false, readErr
	}
	return corecache.EntryValue[string]{Value: string(data), TTL: corecache.NoExpiry}, true, nil
}

// Set writes key's file whole.
func (d dirTier) Set(_ context.Context, key string, entry corecache.EntryValue[string]) error {
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(d.dir, ".value-*")
	if err != nil {
		return err
	}
	if _, writeErr := temporary.WriteString(entry.Value); writeErr != nil {
		_ = temporary.Close()
		return writeErr
	}
	if closeErr := temporary.Close(); closeErr != nil {
		return closeErr
	}
	return os.Rename(temporary.Name(), d.pathOf(key))
}

// Delete removes key's file; an absent one is already deleted.
func (d dirTier) Delete(_ context.Context, key string) error {
	if err := os.Remove(d.pathOf(key)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// InvalidateTag removes nothing: this tier keeps no tag.
func (d dirTier) InvalidateTag(context.Context, string) (int, error) {
	return 0, nil
}
