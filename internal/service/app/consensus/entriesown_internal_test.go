package consensus

import (
	"bytes"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// filledEnts are n entries from index from on at term, each payload of
// payload bytes filled with fill.
func filledEnts(from uint64, n, payload int, term uint64, fill byte) []coreconsensus.EntryValue {
	out := make([]coreconsensus.EntryValue, 0, n)
	for i := range n {
		d := bytes.Repeat([]byte{fill}, payload)
		out = append(out, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: term, Index: from + uint64(i)},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d,
		})
	}
	return out
}

// The entries LogReader.Entries returns own their Data (#1110): fetch and
// readBack keep them without a copy, so nothing the store does afterwards
// may write over them. The row of the log conformance, over both logs and
// the file log's two read paths: 16 entries appended and read, then the
// suffix truncated and appended again with other payloads, the prefix
// compacted, the log grown past it; what was read still equals what was
// appended.
func TestEntriesDataSurvivesTheStoresLaterWrites(t *testing.T) {
	type tc struct {
		name string
		// open returns the log under test.
		open func(t *testing.T) coreconsensus.LogStore
		// synced reads the entries back from the disk rather than the
		// in-memory batch.
		synced bool
	}
	fileLogOf := func(t *testing.T) coreconsensus.LogStore {
		d := openDir(t, t.TempDir(), 64<<10)
		t.Cleanup(func() { _ = d.Close() })
		return d.d.log
	}
	runCase := func(t *testing.T, c tc) {
		const n, payload = 16, 64
		l := c.open(t)
		if err := l.Append(sizedEnts(1, n, payload), nil); err != nil {
			t.Fatal(err)
		}
		if c.synced {
			mustSync(t, l)
		}
		read, err := l.Entries(1, n+1, 1<<20, nil)
		if err != nil || len(read) != n {
			t.Fatalf("Entries: %d entries, %v", len(read), err)
		}
		mustSync(t, l)
		if err := l.TruncateSuffix(9); err != nil {
			t.Fatal(err)
		}
		if err := l.Append(filledEnts(9, 8, payload, 2, 0xEE), nil); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
		if err := l.CompactPrefix(12); err != nil {
			t.Fatal(err)
		}
		if err := l.Append(filledEnts(17, n, payload, 2, 0xDD), nil); err != nil {
			t.Fatal(err)
		}
		mustSync(t, l)
		checkSized(t, read, 1, payload)
	}
	cases := []tc{
		{name: "memory log", open: func(*testing.T) coreconsensus.LogStore { return NewMemoryLog() }},
		{name: "file log, read from its pending batch", open: fileLogOf},
		{name: "file log, read from its segment", open: fileLogOf, synced: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

// The memory double hands out copies, as the file log hands out what it
// read fresh: a caller that keeps the Data of an entry it read never sees a
// later write into the bytes the double was appended (#1110).
func TestTheMemoryLogsEntriesAreCopies(t *testing.T) {
	l := NewMemoryLog()
	in := sizedEnts(1, 4, 32)
	if err := l.Append(in, nil); err != nil {
		t.Fatal(err)
	}
	read, err := l.Entries(1, 5, 1<<20, nil)
	if err != nil || len(read) != 4 {
		t.Fatalf("Entries: %d entries, %v", len(read), err)
	}
	for i := range in {
		clear(in[i].Data)
	}
	checkSized(t, read, 1, 32)
}
