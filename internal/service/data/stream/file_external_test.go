package stream_test

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
	svcstream "github.com/kitsunium/sdk/internal/service/data/stream"
)

// testSegmentBytes is the segment size of every file stream the suite opens
// without naming one: a segment is zero-filled when it is created, and the
// default 64 MiB per partition would make each case write hundreds of them.
const testSegmentBytes int64 = 64 << 10

// openFile opens the file engine over dir, failing the test on a refusal,
// and closes it at the test's end.
func openFile(t *testing.T, dir string, clk clock.Timed, policy corestream.PolicyValue) corestream.Stream {
	t.Helper()
	if policy.SegmentBytes == 0 {
		policy.SegmentBytes = testSegmentBytes
	}
	s, err := svcstream.NewFile(svcstream.FileConfig{Dir: dir, Clock: clk, Policy: policy})
	if err != nil {
		t.Fatalf("NewFile(%s) = %v, want nil", dir, err)
	}
	t.Cleanup(func() { closeStream(t, s) })
	return s
}

// closeStream closes s, which a second Close answers with nil.
func closeStream(t *testing.T, s corestream.Stream) {
	t.Helper()
	if err := s.(io.Closer).Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// segments lists the published segment files of partition p under dir.
func segments(t *testing.T, dir string, p int) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "p"+string(rune('0'+p))))
	if err != nil {
		t.Fatalf("ReadDir() = %v", err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "seg-") && !strings.HasSuffix(entry.Name(), ".tmp") {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestNewFileRefusesWhatItCannotHonour: a policy, a directory and a
// partition count it cannot serve, each refused before anything is written.
func TestNewFileRefusesWhatItCannotHonour(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cfg  svcstream.FileConfig
		code errs.Code
	}{
		{name: "no directory", cfg: svcstream.FileConfig{}, code: corestream.CodeStreamDirectoryUnusable},
		{name: "a file", cfg: svcstream.FileConfig{Dir: file}, code: corestream.CodeStreamDirectoryUnusable},
		{name: "negative policy", cfg: svcstream.FileConfig{Dir: t.TempDir(), Policy: corestream.PolicyValue{RetainFor: -1}}, code: corestream.CodeStreamMisconfigured},
		{name: "record past a segment record", cfg: svcstream.FileConfig{Dir: t.TempDir(), Policy: corestream.PolicyValue{MaxRecordBytes: 1 << 30}}, code: corestream.CodeStreamMisconfigured},
	}
	for _, c := range cases {
		if _, err := svcstream.NewFile(c.cfg); !errs.HasCode(err, c.code) {
			t.Errorf("%s: NewFile() = %v, want code %s", c.name, err, c.code)
		}
	}
}

// TestNewFileRefusesAWorldWritableDirectory: any account could plant or
// unlink a segment.
func TestNewFileRefusesAWorldWritableDirectory(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode bits; its rule reads the DACL, as the queue's does")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := svcstream.NewFile(svcstream.FileConfig{Dir: dir}); !errs.HasCode(err, corestream.CodeStreamDirectoryUnusable) {
		t.Fatalf("NewFile(0777) = %v, want STREAM_DIRECTORY_UNUSABLE", err)
	}
	sub := t.TempDir()
	if err := os.Mkdir(filepath.Join(sub, "p0"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(sub, "p0"), 0o777|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	if _, err := svcstream.NewFile(svcstream.FileConfig{Dir: sub}); !errs.HasCode(err, corestream.CodeStreamDirectoryUnusable) {
		t.Fatalf("NewFile(p0 sticky 0777) = %v, want STREAM_DIRECTORY_UNUSABLE", err)
	}
}

// TestASecondOpenOfADirectoryIsRefusedUntilClose: a segmented log has one
// writer.
func TestASecondOpenOfADirectoryIsRefusedUntilClose(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := openFile(t, dir, nil, corestream.PolicyValue{})
	if _, err := svcstream.NewFile(svcstream.FileConfig{Dir: dir}); !errs.HasCode(err, corestream.CodeStreamDirectoryUnusable) {
		t.Fatalf("second NewFile() = %v, want STREAM_DIRECTORY_UNUSABLE", err)
	}
	closeStream(t, first)
	if _, err := first.Read(t.Context(), 0, 0, 1); !errs.HasCode(err, corestream.CodeStreamBackendFailed) {
		t.Fatalf("Read after Close = %v, want STREAM_BACKEND_FAILED", err)
	}
	openFile(t, dir, nil, corestream.PolicyValue{})
}

// TestAReopenWithFewerPartitionsIsRefused: their records would be lost from
// every key's hash.
func TestAReopenWithFewerPartitionsIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	closeStream(t, openFile(t, dir, nil, corestream.PolicyValue{Partitions: 3}))
	_, err := svcstream.NewFile(svcstream.FileConfig{Dir: dir, Policy: corestream.PolicyValue{Partitions: 2, SegmentBytes: testSegmentBytes}})
	if !errs.HasCode(err, corestream.CodeStreamMisconfigured) {
		t.Fatalf("NewFile(2 of 3) = %v, want STREAM_MISCONFIGURED", err)
	}
	openFile(t, dir, nil, corestream.PolicyValue{Partitions: 4})
}

// TestAReopenedStreamKeepsItsRecordsItsTrimAndItsCursors: everything a
// stream answers is on the device.
func TestAReopenedStreamKeepsItsRecordsItsTrimAndItsCursors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	policy := corestream.PolicyValue{Partitions: 2}
	s := openFile(t, dir, clk, policy)
	key := keyOn(t, 1, 2)
	appendOK(t, s, keyed(key, "a"), keyed(key, "b"), keyed(key, "c"))
	clk.Advance(time.Minute)
	appendOK(t, s, keyed(key, "d"))
	if err := s.(corestream.Trimmer).Trim(t.Context(), 1, 1); err != nil {
		t.Fatalf("Trim(1) = %v", err)
	}
	if err := s.(corestream.Committer).Commit(t.Context(), "billing", 1, 3); err != nil {
		t.Fatalf("Commit() = %v", err)
	}
	closeStream(t, s)

	again := openFile(t, dir, clk, policy)
	if bounds := boundsOK(t, again, 1); bounds != (corestream.BoundsValue{First: 1, Next: 4}) {
		t.Fatalf("Bounds after reopen = %+v, want {1 4}", bounds)
	}
	entries := readOK(t, again, 1, 1, 10)
	if len(entries) != 3 || string(entries[0].Record.Payload) != "b" || string(entries[2].Record.Key) != key {
		t.Fatalf("Read after reopen = %+v", entries)
	}
	if !entries[2].AppendedAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("AppendedAt after reopen = %s, want %s", entries[2].AppendedAt, epoch.Add(time.Minute))
	}
	if at, err := again.(corestream.Seeker).OffsetAt(t.Context(), 1, epoch.Add(time.Second)); err != nil || at != 3 {
		t.Fatalf("OffsetAt after reopen = %d, %v, want 3", at, err)
	}
	if next, err := again.(corestream.Committer).Committed(t.Context(), "billing", 1); err != nil || next != 3 {
		t.Fatalf("Committed after reopen = %d, %v, want 3", next, err)
	}
	if got := appendOK(t, again, keyed(key, "e")); got.First != 4 {
		t.Fatalf("Append after reopen took offset %d, want 4", got.First)
	}
}

// TestASegmentOlderThanRetainForIsUnlinked: the age bound releases whole
// sealed segments, at the Append that finds them expired, and a read below
// the retained range is refused.
func TestASegmentOlderThanRetainForIsUnlinked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(epoch)
	s := openFile(t, dir, clk, corestream.PolicyValue{RetainFor: time.Hour, SegmentBytes: 4096})
	payload := strings.Repeat("x", 1000)
	appendOK(t, s, record(payload), record(payload), record(payload))
	appendOK(t, s, record(payload), record(payload), record(payload))
	before := segments(t, dir, 0)
	if len(before) < 2 {
		t.Fatalf("segments after a rotation = %v, want two at least", before)
	}
	clk.Advance(time.Hour)
	fresh := appendOK(t, s, record("fresh"))
	after := segments(t, dir, 0)
	for _, name := range after {
		if name == before[0] {
			t.Fatalf("segment %s, older than RetainFor, is still on disk: %v", name, after)
		}
	}
	if bounds := boundsOK(t, s, 0); bounds.First != fresh.First {
		t.Fatalf("Bounds = %+v, want First %d", bounds, fresh.First)
	}
	if _, err := s.Read(t.Context(), 0, 0, 10); !errs.HasCode(err, corestream.CodeOffsetTrimmed) {
		t.Fatalf("Read(0) below Bounds().First = %v, want OFFSET_TRIMMED", err)
	}
}

// TestRetainBytesIsAppliedAgainAfterAReopen: the byte bound and the newest
// batch it spares are read back from the segments.
func TestRetainBytesIsAppliedAgainAfterAReopen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	policy := corestream.PolicyValue{RetainBytes: 10}
	s := openFile(t, dir, nil, policy)
	appendOK(t, s, record("aaaa"), record("bbbb"))
	appendOK(t, s, record("cccccccccccc"))
	closeStream(t, s)
	again := openFile(t, dir, nil, policy)
	if bounds := boundsOK(t, again, 0); bounds != (corestream.BoundsValue{First: 2, Next: 3}) {
		t.Fatalf("Bounds after reopen = %+v, want {2 3}", bounds)
	}
}
