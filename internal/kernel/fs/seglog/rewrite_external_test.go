package seglog_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// segmentSizes returns the size of every published segment of dir.
func segmentSizes(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(entry.Name()) == "" {
			sizes[entry.Name()] = info.Size()
		}
	}
	return sizes
}

// TestRewriteChangesRecordsDurablyAndKeepsTheirIndexes: the records edit
// changes read back changed, in memory and after a reopen; every other
// record reads as it was written; the indexes do not move; a sealed
// segment shrinks and the last one keeps taking appends.
func TestRewriteChangesRecordsDurablyAndKeepsTheirIndexes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := open(t, dir, 1024)
	appendRange(t, l, 0, 120)
	before := segmentSizes(t, dir)
	marker := []byte("x")
	even := func(index uint64, kind uint8, data []byte) (uint8, []byte, bool) {
		if index%2 != 0 {
			return 0, nil, false
		}
		if !bytes.Equal(data, payload(index)) || kind != uint8(index) {
			t.Errorf("edit(%d) saw kind %d payload %q", index, kind, data)
		}
		return kind + 1, marker, true
	}
	n, err := l.Rewrite(100, even)
	if err != nil {
		t.Fatalf("Rewrite = %v", err)
	}
	if n == 0 {
		t.Fatal("Rewrite rewrote no segment")
	}
	verify := func(l interface {
		Read(uint64) ([]byte, error)
		Kind(uint64) (uint8, error)
	},
	) {
		t.Helper()
		for i := uint64(0); i < 120; i++ {
			got, err := l.Read(i)
			if err != nil {
				t.Fatalf("read %d: %v", i, err)
			}
			kind, _ := l.Kind(i)
			if i < 100 && i%2 == 0 {
				if !bytes.Equal(got, marker) || kind != uint8(i)+1 {
					t.Fatalf("read %d = %q kind %d, want the rewrite", i, got, kind)
				}
				continue
			}
			if !bytes.Equal(got, payload(i)) || kind != uint8(i) {
				t.Fatalf("read %d = %q kind %d, want the original", i, got, kind)
			}
		}
	}
	verify(l)
	shrunk := false
	for name, size := range segmentSizes(t, dir) {
		if size < before[name] {
			shrunk = true
		}
	}
	if !shrunk {
		t.Fatal("no segment is smaller after the rewrite")
	}
	appendRange(t, l, 120, 130)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 1024)
	defer func() { _ = l.Close() }()
	verify(l)
	check(t, l, 120, 130)
	if first, next := l.Bounds(); first != 0 || next != 130 {
		t.Fatalf("Bounds = %d, %d, want 0, 130", first, next)
	}
}

// TestRewriteThatChangesNothingTouchesNothing: no segment is written when
// edit changes no record, and a record below the first index is never
// offered.
func TestRewriteThatChangesNothingTouchesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := open(t, dir, 1024)
	defer func() { _ = l.Close() }()
	appendRange(t, l, 0, 60)
	if err := l.DropBefore(10); err != nil {
		t.Fatal(err)
	}
	var seen []uint64
	n, err := l.Rewrite(1000, func(index uint64, _ uint8, _ []byte) (uint8, []byte, bool) {
		seen = append(seen, index)
		return 0, nil, false
	})
	if err != nil || n != 0 {
		t.Fatalf("Rewrite = %d, %v, want 0, nil", n, err)
	}
	if len(seen) != 50 || seen[0] != 10 || seen[49] != 59 {
		t.Fatalf("edit offered %d records from %v, want 10 through 59", len(seen), seen[:min(len(seen), 1)])
	}
	check(t, l, 10, 60)
}

// TestRewriteSyncsAPendingBatchFirst: a batch appended and not synced is
// made durable before the rewrite reads the segment it lies in.
func TestRewriteSyncsAPendingBatchFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	l := open(t, dir, 4096)
	appendRange(t, l, 0, 5)
	if err := l.Append(5, 5, payload(5)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Rewrite(6, func(index uint64, kind uint8, _ []byte) (uint8, []byte, bool) {
		return kind, []byte("gone"), index == 5
	}); err != nil {
		t.Fatalf("Rewrite = %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l = open(t, dir, 4096)
	defer func() { _ = l.Close() }()
	if got, err := l.Read(5); err != nil || string(got) != "gone" {
		t.Fatalf("read 5 = %q, %v, want the rewrite", got, err)
	}
	check(t, l, 0, 5)
}
