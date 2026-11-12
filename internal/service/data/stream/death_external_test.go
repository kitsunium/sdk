package stream_test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// victimDirEnv carries the stream directory to the child process. Its
// presence is what turns [TestTheVictimAppender] from a skip into a run, so
// the helper is discovered by `go test ./...` like any other test and needs
// no build tag, no `manual` target and no compensating lane (rule 12) — the
// queue's victim, ported.
const victimDirEnv = "KTNS_VICTIM_STREAM_DIR"

// ackedMarker opens every line the child prints once an Append returned nil.
const ackedMarker = "KTNS-ACKED "

// victimBatch is how many records each of the child's batches holds: the
// parent checks that a batch survives whole or not at all.
const victimBatch = 3

// victimAcks is how many acknowledged batches the parent waits for before it
// kills the child; the child goes on appending, so the kill lands inside an
// Append more often than not.
const victimAcks = 40

// TestAKilledAppenderLosesNoAcknowledgedRecord is Append's durability claim,
// proved by killing a real producer in a real other process with a signal it
// cannot catch: every batch whose Append returned nil before the kill is read
// back by the parent, whole, in its order, and no batch is read back in part.
// The directory's lock dies with the child, which is why the parent can open
// it at all.
func TestAKilledAppenderLosesNoAcknowledgedRecord(t *testing.T) {
	requireFileEngine(t)
	//: not parallel: it spawns a process and waits on it.
	dir := t.TempDir()
	victim, lines := startVictim(t, dir)
	acked := -1
	for acked+1 < victimAcks && lines.Scan() {
		line := lines.Text()
		if !strings.HasPrefix(line, ackedMarker) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(line, ackedMarker))
		if err != nil || n != acked+1 {
			t.Fatalf("the child acknowledged %q after batch %d", line, acked)
		}
		acked = n
	}
	if acked+1 < victimAcks {
		t.Fatalf("the child acknowledged %d batches before its output ended, want %d", acked+1, victimAcks)
	}
	if err := victim.Process.Kill(); err != nil {
		t.Fatalf("Kill() = %v, want nil", err)
	}
	if err := victim.Wait(); err == nil {
		t.Fatal("the victim exited cleanly; a SIGKILLed process must not")
	}

	s := openFile(t, dir, nil, victimPolicy())
	entries := readOK(t, s, 0, 0, 1<<20)
	if len(entries) < (acked+1)*victimBatch {
		t.Fatalf("read %d records back, want at least the %d acknowledged", len(entries), (acked+1)*victimBatch)
	}
	if len(entries)%victimBatch != 0 {
		t.Fatalf("read %d records back: a batch of %d survived in part", len(entries), victimBatch)
	}
	for i, entry := range entries {
		want := strconv.Itoa(i/victimBatch) + "/" + strconv.Itoa(i%victimBatch)
		if got := string(entry.Record.Payload); got != want || entry.Offset != corestream.Offset(i) {
			t.Fatalf("record %d = %q at offset %d, want %q", i, got, entry.Offset, want)
		}
	}
}

// TestTheVictimAppender is the child: it appends numbered batches forever,
// printing each one Append acknowledged, until it is killed.
func TestTheVictimAppender(t *testing.T) {
	dir := os.Getenv(victimDirEnv)
	if dir == "" {
		t.Skip("not the victim: " + victimDirEnv + " is unset")
	}
	s, err := svcstream.NewFile(svcstream.FileConfig{Dir: dir, Policy: victimPolicy()})
	if err != nil {
		t.Fatalf("NewFile() = %v, want nil", err)
	}
	for n := 0; ; n++ {
		batch := make([]corestream.RecordValue, victimBatch)
		for i := range batch {
			batch[i] = record(strconv.Itoa(n) + "/" + strconv.Itoa(i))
		}
		if _, appendErr := s.Append(t.Context(), batch); appendErr != nil {
			t.Fatalf("Append(%d) = %v", n, appendErr)
		}
		//: os.Stdout is unbuffered: the parent reads the line as soon as it is written.
		fmt.Fprintln(os.Stdout, ackedMarker+strconv.Itoa(n)) //nolint:forbidigo
	}
}

// victimPolicy is the policy both processes open the stream with.
func victimPolicy() corestream.PolicyValue {
	return corestream.PolicyValue{SegmentBytes: testSegmentBytes}
}

// startVictim launches this test binary again, running only the victim, and
// returns its stdout line by line.
func startVictim(t *testing.T, dir string) (*exec.Cmd, *bufio.Scanner) {
	t.Helper()
	child := exec.Command(os.Args[0], "-test.run=^TestTheVictimAppender$", "-test.v") //nolint:gosec
	child.Env = append(os.Environ(), victimDirEnv+"="+dir)
	child.Stderr = io.Discard
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe() = %v, want nil", err)
	}
	if err := child.Start(); err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}
	t.Cleanup(func() {
		if killErr := child.Process.Kill(); killErr != nil {
			t.Logf("the victim was already gone at cleanup: %v", killErr)
		}
	})
	//: a child that never acknowledges would hang the scan: bound it.
	timer := time.AfterFunc(2*time.Minute, func() { _ = child.Process.Kill() })
	t.Cleanup(func() { timer.Stop() })
	return child, bufio.NewScanner(pipe)
}
