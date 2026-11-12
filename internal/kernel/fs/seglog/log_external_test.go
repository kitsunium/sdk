package seglog_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/fs/seglog"
)

// payload is the deterministic payload of index i.
func payload(i uint64) []byte { return []byte(fmt.Sprintf("record-%d", i)) }

// open opens dir with segments of seg bytes, failing the test on an error.
func open(t *testing.T, dir string, seg int64) *seglog.Log {
	t.Helper()
	l, err := seglog.Open(dir, seg)
	if err != nil {
		t.Fatalf("open: %v %v", err, errs.FieldsOf(err))
	}
	return l
}

// appendRange appends lo through hi-1, each its own kind byte, synced in
// batches of at most twenty.
func appendRange(t *testing.T, l *seglog.Log, lo, hi uint64) {
	t.Helper()
	for i := lo; i < hi; i++ {
		if err := l.Append(i, uint8(i), payload(i)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if (i+1)%20 == 0 {
			if err := l.Sync(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
}

// check reads lo through hi-1 one by one and as one range.
func check(t *testing.T, l *seglog.Log, lo, hi uint64) {
	t.Helper()
	for i := lo; i < hi; i++ {
		got, err := l.Read(i)
		if err != nil || !bytes.Equal(got, payload(i)) {
			t.Fatalf("read %d = %q, %v", i, got, err)
		}
		if k, err := l.Kind(i); err != nil || k != uint8(i) {
			t.Fatalf("kind %d = %d, %v", i, k, err)
		}
	}
	got, err := l.Range(lo, hi, 1<<30, nil)
	if err != nil || len(got) != int(hi-lo) {
		t.Fatalf("range %d..%d: %d records, %v", lo, hi, len(got), err)
	}
	for j, p := range got {
		if !bytes.Equal(p, payload(lo+uint64(j))) {
			t.Fatalf("range record %d = %q", lo+uint64(j), p)
		}
	}
}

// segments counts the published segment files of dir.
func segments(t *testing.T, dir string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "seg-????????????????"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

func TestAFreshLogIsEmptyAtZero(t *testing.T) {
	l := open(t, filepath.Join(t.TempDir(), "log"), 4096)
	defer l.Close()
	if f, n := l.Bounds(); f != 0 || n != 0 {
		t.Fatalf("bounds %d %d", f, n)
	}
	if _, err := l.Read(0); !errors.Is(err, seglog.OutOfRange) {
		t.Fatalf("read of an empty log: %v", err)
	}
	if err := l.Sync(); err != nil {
		t.Fatalf("an empty sync: %v", err)
	}
}

func TestRecordsPersistAcrossReopenAndRotate(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, 4096)
	appendRange(t, l, 0, 200)
	check(t, l, 0, 200)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if n := segments(t, dir); n < 3 {
		t.Fatalf("%d segment(s) for 200 records of 4 KiB segments, want a rotation", n)
	}
	l = open(t, dir, 4096)
	defer l.Close()
	if f, n := l.Bounds(); f != 0 || n != 200 {
		t.Fatalf("bounds after reopen %d %d", f, n)
	}
	check(t, l, 0, 200)
	appendRange(t, l, 200, 210)
	check(t, l, 0, 210)
}

func TestAppendedRecordsAreReadableBeforeTheirSync(t *testing.T) {
	l := open(t, t.TempDir(), 4096)
	defer l.Close()
	appendRange(t, l, 0, 3)
	for i := uint64(3); i < 6; i++ {
		if err := l.Append(i, uint8(i), payload(i)); err != nil {
			t.Fatal(err)
		}
	}
	check(t, l, 0, 6)
}

func TestAppendRefusesAnIndexOutOfSequence(t *testing.T) {
	l := open(t, t.TempDir(), 4096)
	defer l.Close()
	cases := []uint64{1, 7}
	for _, i := range cases {
		if err := l.Append(i, 0, nil); !errors.Is(err, seglog.OutOfSequence) {
			t.Fatalf("append %d on an empty log: %v", i, err)
		}
	}
	if err := l.Append(0, 0, nil); err != nil {
		t.Fatal(err)
	}
}

func TestABatchLargerThanASegmentGetsItsOwnSegment(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, 4096)
	big := bytes.Repeat([]byte{7}, 64<<10)
	if err := l.Append(0, 1, big); err != nil {
		t.Fatal(err)
	}
	if err := l.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 4096)
	defer l.Close()
	got, err := l.Read(0)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("read back %d bytes, %v", len(got), err)
	}
}

func TestRangeStopsAtMaxBytesButReturnsOne(t *testing.T) {
	l := open(t, t.TempDir(), 1<<16)
	defer l.Close()
	appendRange(t, l, 0, 10)
	one := len(payload(0))
	got, err := l.Range(0, 10, 0, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("maxBytes 0: %d records, %v", len(got), err)
	}
	got, err = l.Range(0, 10, 3*one, nil)
	if err != nil || len(got) != 3 {
		t.Fatalf("maxBytes of three: %d records, %v", len(got), err)
	}
	if got, err := l.Range(4, 4, 100, nil); err != nil || len(got) != 0 {
		t.Fatalf("an empty range: %d, %v", len(got), err)
	}
	if _, err := l.Range(5, 11, 100, nil); !errors.Is(err, seglog.OutOfRange) {
		t.Fatalf("a range past the end: %v", err)
	}
	// A payload returned by Range is the caller's: appending to it must not
	// write over the next one.
	got, _ = l.Range(0, 2, 1<<20, nil)
	_ = append(got[0], 'x')
	if !bytes.Equal(got[1], payload(1)) {
		t.Fatalf("an append to one payload changed the next: %q", got[1])
	}
}

func TestDropBeforeUnlinksWholeSegmentsAndSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, 4096)
	appendRange(t, l, 0, 300)
	before := segments(t, dir)
	if err := l.DropBefore(250); err != nil {
		t.Fatal(err)
	}
	if f, n := l.Bounds(); f != 250 || n != 300 {
		t.Fatalf("bounds after the drop %d %d", f, n)
	}
	if _, err := l.Read(249); !errors.Is(err, seglog.Dropped) {
		t.Fatalf("read below first: %v", err)
	}
	if after := segments(t, dir); after >= before {
		t.Fatalf("no segment unlinked: %d then %d", before, after)
	}
	check(t, l, 250, 300)
	if err := l.DropBefore(10); err != nil {
		t.Fatalf("a drop below the first: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 4096)
	if f, n := l.Bounds(); f != 250 || n != 300 {
		t.Fatalf("bounds after reopen %d %d", f, n)
	}
	check(t, l, 250, 300)
	// Past the end: the log is empty and starts there.
	if err := l.DropBefore(1000); err != nil {
		t.Fatal(err)
	}
	if f, n := l.Bounds(); f != 1000 || n != 1000 {
		t.Fatalf("bounds after dropping past the end %d %d", f, n)
	}
	appendRange(t, l, 1000, 1005)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 4096)
	defer l.Close()
	if f, n := l.Bounds(); f != 1000 || n != 1005 {
		t.Fatalf("bounds after the second reopen %d %d", f, n)
	}
	check(t, l, 1000, 1005)
}

func TestACallAfterCloseIsRefused(t *testing.T) {
	l := open(t, t.TempDir(), 4096)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	if err := l.Append(0, 0, nil); !errors.Is(err, seglog.Closed) {
		t.Fatalf("append after close: %v", err)
	}
	if _, err := l.Read(0); !errors.Is(err, seglog.Closed) {
		t.Fatalf("read after close: %v", err)
	}
}

func TestAnUnsyncedBatchIsDiscardedByClose(t *testing.T) {
	dir := t.TempDir()
	l := open(t, dir, 4096)
	appendRange(t, l, 0, 2)
	if err := l.Append(2, 0, payload(2)); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 4096)
	defer l.Close()
	if _, n := l.Bounds(); n != 2 {
		t.Fatalf("next %d after closing a pending batch, want 2", n)
	}
}

func TestReadersRunBesideTheWriter(t *testing.T) {
	l := open(t, t.TempDir(), 8192)
	defer l.Close()
	appendRange(t, l, 0, 1)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, n := l.Bounds()
				if _, err := l.Range(0, n, 1<<20, nil); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	for i := uint64(1); i < 100; i++ {
		appendRange(t, l, i, i+1)
	}
	close(stop)
	wg.Wait()
	check(t, l, 0, 100)
}

func TestOpenRefusesADirectoryItCannotCreate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := seglog.Open(filepath.Join(file, "log"), 4096); !errors.Is(err, seglog.Failed) {
		t.Fatalf("open under a file: %v", err)
	}
}
