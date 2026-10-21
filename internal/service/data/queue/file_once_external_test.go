package queue_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"

	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// The file broker's PublishOnce reservations, as files: once/<sha256>.key,
// holding the end of the window (ADR 0183).

// reservationName is the file a key's reservation lives in.
func reservationName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + ".key"
}

// onceEntries lists what the reservations' directory holds, sorted.
func onceEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "once"))
	if err != nil {
		t.Fatalf("ReadDir(once) = %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// TestAFileReservationIsOneFilePerKeySweptOnceItsWindowPassed pins the shape
// on disk: a key's reservation is a file named by its SHA-256 — never by the
// key, which a caller may derive from the payload — holding the end of its
// window in nanoseconds; and a passed one is removed by a later PublishOnce
// of another key, once the broker's sweep is due.
func TestAFileReservationIsOneFilePerKeySweptOnceItsWindowPassed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	publishOnce(t, broker, "customer-42@example.org", "a", time.Minute)
	if got, want := onceEntries(t, dir), []string{reservationName("customer-42@example.org")}; !slices.Equal(got, want) {
		t.Fatalf("once/ = %v, want %v", got, want)
	}
	content, err := os.ReadFile(filepath.Join(dir, "once", reservationName("customer-42@example.org")))
	if err != nil {
		t.Fatalf("ReadFile() = %v", err)
	}
	if want := strconv.FormatInt(epoch.Add(time.Minute).UnixNano(), 10); string(content) != want {
		t.Fatalf("the reservation holds %q, want the window's end %q", content, want)
	}
	clk.Advance(2 * time.Minute)
	publishOnce(t, broker, "b", "b", time.Minute)
	if got, want := onceEntries(t, dir), []string{reservationName("b")}; !slices.Equal(got, want) {
		t.Fatalf("once/ after the sweep = %v, want only b's %v", got, want)
	}
}

// TestAHalfWrittenReservationReadsAsPassed pins what a crash between the
// create and the write leaves: a reservation with no end it can read, which
// holds nothing — the next publication under its key goes out and reserves
// the key properly. A crash costs at most a duplicate, never a lost message.
func TestAHalfWrittenReservationReadsAsPassed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	planted := filepath.Join(dir, "once", reservationName("k"))
	if err := os.WriteFile(planted, []byte("12"), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	if _, published := publishOnce(t, broker, "k", "x", time.Minute); !published {
		t.Fatal("a half-written reservation held its key")
	}
	if _, published := publishOnce(t, broker, "k", "x", time.Minute); published {
		t.Fatal("the reservation that replaced it did not hold its key")
	}
}

// TestTwoFileBrokersOverOneDirectoryShareTheirReservations is the
// inter-process claim applied to the window: a reservation is a file, so a
// second broker over the same directory — in another process, the same way —
// sees the key held and publishes nothing.
func TestTwoFileBrokersOverOneDirectoryShareTheirReservations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	first := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	second := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	if _, published := publishOnce(t, first, "k", "x", time.Minute); !published {
		t.Fatal("the first broker published nothing")
	}
	if _, published := publishOnce(t, second, "k", "x", time.Minute); published {
		t.Fatal("the second broker published under a key the first holds")
	}
	if batch := receive(t, second, 10); len(batch) != 1 {
		t.Fatalf("the queue holds %d messages, want 1", len(batch))
	}
}

// TestAReservationSetAsideByACrashIsSweptAndARunningOneIsNot pins the sweep's
// care with the names a takeover leaves: a passed reservation set aside and
// never removed — a crash between the rename and the unlink — is removed, and
// one still running is left for the producer putting it back.
func TestAReservationSetAsideByACrashIsSweptAndARunningOneIsNot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	broker := requireFileBroker(t, svcqueue.FileConfig{Dir: dir, Policy: defaultPolicy(), Clock: clk})
	passed := filepath.Join(dir, "once", "aa.00000000.gone")
	running := filepath.Join(dir, "once", "bb.00000000.gone")
	if err := os.WriteFile(passed, []byte(strconv.FormatInt(epoch.UnixNano(), 10)), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	if err := os.WriteFile(running, []byte(strconv.FormatInt(epoch.Add(time.Hour).UnixNano(), 10)), 0o600); err != nil {
		t.Fatalf("WriteFile() = %v", err)
	}
	publishOnce(t, broker, "k", "x", time.Minute)
	want := []string{"bb.00000000.gone", reservationName("k")}
	slices.Sort(want)
	if got := onceEntries(t, dir); !slices.Equal(got, want) {
		t.Fatalf("once/ = %v, want %v", got, want)
	}
}
