package consensus

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// #1114: the conformance tables. Three ports have two engines each, a memory
// double that claims every port contract but durability (D2) and the shipped
// engine, and most of this package's suites run one or the other by
// scenario. Each table below runs one row per sentence of the port's doc in
// internal/core/app/consensus/gen_ports.go over every engine of that port,
// on the shape of the queue domain's everyBroker: a double that drifts from
// the shipped engine fails here, on its own row, naming the sentence. A row
// that needs durability is skipped on the double, the sentence in the
// reason; TestConformanceRowsQuoteThePorts holds every quoted sentence to
// the port file.

// portsSource is the port file every row quotes, relative to this package.
const portsSource = "internal/core/app/consensus/gen_ports.go"

// skipUndurable skips a row the double cannot hold: it keeps nothing past
// the process, by design (D2).
func skipUndurable(t *testing.T, sentence, engine string) {
	t.Helper()
	t.Skipf("%q: %s has no durability (D2); the row holds the shipped engine alone", sentence, engine)
}

// ---- LogStore ----

// logEngine is one implementation of LogStore under test. reopen, nil for
// an engine with no durability, closes the store and opens it again over
// the same state.
type logEngine struct {
	name string
	open func(t *testing.T) (coreconsensus.LogStore, func(t *testing.T) coreconsensus.LogStore)
}

// everyLog is every LogStore the package ships: the memory double and the
// segmented file log, on 64 KiB segments over a temporary data directory.
func everyLog() []logEngine {
	return []logEngine{
		{name: "memory", open: func(*testing.T) (coreconsensus.LogStore, func(*testing.T) coreconsensus.LogStore) {
			return NewMemoryLog(), nil
		}},
		{name: "file", open: func(t *testing.T) (coreconsensus.LogStore, func(*testing.T) coreconsensus.LogStore) {
			dir := t.TempDir()
			d := openDir(t, dir, 64<<10)
			t.Cleanup(func() { abandon(d.Close()) })
			reopen := func(t *testing.T) coreconsensus.LogStore {
				t.Helper()
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				d = openDir(t, dir, 64<<10)
				return d.Log()
			}
			return d.Log(), reopen
		}},
	}
}

// logRow is one sentence of LogReader or LogStore, held on one engine.
type logRow struct {
	sentence string
	detail   string
	durable  bool
	run      func(t *testing.T, l coreconsensus.LogStore, reopen func(*testing.T) coreconsensus.LogStore)
}

// confData is the payload of entry i of term: every payload is
// confDataLen bytes, so a maxBytes is a count of entries.
func confData(i, term uint64) []byte { return fmt.Appendf(nil, "e%07d-t%07d", i, term) }

const confDataLen = 17

// confEntries returns the entries from..to of term, each of confDataLen bytes.
func confEntries(from, to, term uint64) []coreconsensus.EntryValue {
	var out []coreconsensus.EntryValue
	for i := from; i <= to; i++ {
		d := confData(i, term)
		out = append(out, coreconsensus.EntryValue{
			Position: coreconsensus.PositionValue{Term: term, Index: i},
			Kind:     coreconsensus.EntryCommand, BodySum: wire.BodySum(d), Data: d,
		})
	}
	return out
}

func confAppend(t *testing.T, l coreconsensus.LogStore, es []coreconsensus.EntryValue, hs *coreconsensus.HardState) {
	t.Helper()
	if err := l.Append(es, hs); err != nil {
		t.Fatalf("Append: %v %v", err, errs.FieldsOf(err))
	}
}

func confSync(t *testing.T, l coreconsensus.LogStore) {
	t.Helper()
	if err := l.Sync(); err != nil {
		t.Fatalf("Sync: %v %v", err, errs.FieldsOf(err))
	}
}

func confCompact(t *testing.T, l coreconsensus.LogStore, through, term uint64) {
	t.Helper()
	if err := compactLog(l, through, term); err != nil {
		t.Fatalf("compact through %d: %v %v", through, err, errs.FieldsOf(err))
	}
}

// wantEntries reads [lo, hi) under maxBytes and requires exactly the
// indexes want, each with its own payload of term.
func wantEntries(t *testing.T, l coreconsensus.LogReader, lo, hi uint64, maxBytes int, term uint64, want ...uint64) {
	t.Helper()
	got, err := l.Entries(lo, hi, maxBytes, nil)
	if err != nil {
		t.Fatalf("Entries(%d, %d, %d): %v %v", lo, hi, maxBytes, err, errs.FieldsOf(err))
	}
	var idx []uint64
	for _, e := range got {
		idx = append(idx, e.Position.Index)
	}
	if fmt.Sprint(idx) != fmt.Sprint(want) {
		t.Fatalf("Entries(%d, %d, %d) returned indexes %v, want %v", lo, hi, maxBytes, idx, want)
	}
	for _, e := range got {
		if !bytes.Equal(e.Data, confData(e.Position.Index, term)) || e.Position.Term != term {
			t.Fatalf("entry %d: term %d data %q, want term %d data %q", e.Position.Index, e.Position.Term, e.Data, term, confData(e.Position.Index, term))
		}
	}
}

func wantBounds(t *testing.T, l coreconsensus.LogReader, first, last uint64) {
	t.Helper()
	if f, la := l.Bounds(); f != first || la != last {
		t.Fatalf("Bounds() = (%d, %d), want (%d, %d)", f, la, first, last)
	}
}

func wantTerm(t *testing.T, l coreconsensus.LogReader, index, term uint64) {
	t.Helper()
	if got, err := l.Term(index); err != nil || got != term {
		t.Fatalf("Term(%d) = %d %v, want %d", index, got, err, term)
	}
}

func idxs(from, to uint64) []uint64 {
	var out []uint64
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

const (
	sEntries     = "Entries returns entries in [lo, hi), at most maxBytes, and always at least one entry when lo < hi, even above maxBytes."
	sLoCompacted = "lo below first is COMPACTED."
	sCompacted   = "Expected outcomes are answers, not failures: an index below the first retained one is COMPACTED."
	sTermFirst   = "Term answers for first-1 too, from the term kept at compaction."
	sBoundsEmpty = "Bounds of an empty log after a snapshot at s are (s+1, s)."
	sUnsynced    = "readers see appended entries before they are synced."
	sOneWriter   = "One writer, many readers."
	sAppend      = "Append buffers entries and, when hs is not nil, a hard state, in ONE record batch."
	sSync        = "Sync makes everything appended so far durable."
	sPowerLoss   = "When Sync and WriteStamp return nil, what they cover survives a power loss"
	sTruncate    = "TruncateSuffix writes ONE durable truncate record removing every entry from index from on, and nothing else."
	sCompact     = "CompactPrefix drops what a snapshot covers, through index through."
	sHardState   = "HardState returns the last hard state appended."
	sStamp       = "Stamp returns the directory's stamp."
	sWriteStamp  = "WriteStamp writes the stamp, durable on return."
)

// confStamp is a stamp every field of which is set.
var confStamp = coreconsensus.StampValue{
	Log: coreconsensus.LogIdentityValue{Cluster: coreconsensus.ClusterID{0x11, 0x14}},
	ID:  7, Incarnation: coreconsensus.Incarnation{9}, Slot: "site-c", State: coreconsensus.StampState(1), Founding: [32]byte{1, 1, 1, 4},
}

// logRows are the LogReader and LogStore sentences, one row per behaviour.
func logRows() []logRow {
	type reopenFn = func(*testing.T) coreconsensus.LogStore
	return []logRow{
		{sEntries, "a range is read in order, its hi excluded", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			wantEntries(t, l, 3, 7, 1<<20, 1, idxs(3, 6)...)
		}},
		{sEntries, "an empty range [lo, lo) reads nothing", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 4, 1), nil)
			wantEntries(t, l, 3, 3, 1<<20, 1)
		}},
		{sEntries, "maxBytes cuts the range at a whole entry", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			wantEntries(t, l, 2, 10, 3*confDataLen, 1, 2, 3, 4)
			wantEntries(t, l, 2, 10, 3*confDataLen-1, 1, 2, 3)
		}},
		{sEntries, "one entry when even the first is above maxBytes", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 6, 1), nil)
			wantEntries(t, l, 2, 6, 1, 1, 2)
			wantEntries(t, l, 4, 6, 0, 1, 4)
		}},
		{sEntries, "the answer is appended to dst", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 3, 1), nil)
			dst := []coreconsensus.EntryValue{{Position: coreconsensus.PositionValue{Index: 99}}}
			got, err := l.Entries(1, 4, 1<<20, dst)
			if err != nil || len(got) != 4 || got[0].Position.Index != 99 || got[1].Position.Index != 1 || got[3].Position.Index != 3 {
				t.Fatalf("Entries over a dst of one: %d entries %v", len(got), err)
			}
		}},
		{sLoCompacted, "Entries from below the first retained index", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confCompact(t, l, 4, 0)
			if _, err := l.Entries(2, 6, 1<<20, nil); !errors.Is(err, coreconsensus.Compacted) {
				t.Fatalf("Entries(2, 6) after a compaction through 4: %v, want COMPACTED", err)
			}
			wantEntries(t, l, 5, 7, 1<<20, 1, 5, 6)
		}},
		{sCompacted, "COMPACTED carries the index asked and the first retained one", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confCompact(t, l, 4, 0)
			_, err := l.Entries(2, 6, 1<<20, nil)
			if fieldOf(err, "index") != "2" || fieldOf(err, "first") != "5" {
				t.Fatalf("COMPACTED fields %v, want index 2 and first 5", errs.FieldsOf(err))
			}
		}},
		{sCompacted, "Term below first-1 is COMPACTED, an answer", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confCompact(t, l, 4, 0)
			if _, err := l.Term(3); !errors.Is(err, coreconsensus.Compacted) || errors.Is(err, coreconsensus.StorageFailed) {
				t.Fatalf("Term(3) after a compaction through 4: %v, want COMPACTED", err)
			}
		}},
		{sTermFirst, "the term of the last compacted entry", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 4, 1), nil)
			confAppend(t, l, confEntries(5, 8, 2), nil)
			confCompact(t, l, 6, 0)
			wantTerm(t, l, 6, 2)
			confCompact(t, l, 7, 0)
			wantTerm(t, l, 7, 2)
		}},
		{sTermFirst, "0 at index 0 of a log never compacted", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			wantTerm(t, l, 0, 0)
			confAppend(t, l, confEntries(1, 2, 3), nil)
			wantTerm(t, l, 0, 0)
		}},
		{sTermFirst, "every retained index answers its entry's term", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 3, 1), nil)
			confAppend(t, l, confEntries(4, 6, 4), nil)
			for i, want := range map[uint64]uint64{1: 1, 3: 1, 4: 4, 6: 4} {
				wantTerm(t, l, i, want)
			}
		}},
		{sBoundsEmpty, "s = 0 for a log never written", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			wantBounds(t, l, 1, 0)
		}},
		{sBoundsEmpty, "a compaction through the last entry", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 5, 2), nil)
			confCompact(t, l, 5, 0)
			wantBounds(t, l, 6, 5)
			wantTerm(t, l, 5, 2)
		}},
		{sBoundsEmpty, "a compaction past the end, at the snapshot's term", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 5, 2), nil)
			confCompact(t, l, 20, 3)
			wantBounds(t, l, 21, 20)
			wantTerm(t, l, 20, 3)
		}},
		{sCompact, "the next entry after a compaction past the end is s+1", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 5, 2), nil)
			confCompact(t, l, 20, 3)
			confAppend(t, l, confEntries(21, 22, 3), nil)
			wantBounds(t, l, 21, 22)
			wantEntries(t, l, 21, 23, 1<<20, 3, 21, 22)
		}},
		{sCompact, "the entries through index go, those after stay", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confCompact(t, l, 4, 0)
			wantBounds(t, l, 5, 10)
			wantEntries(t, l, 5, 11, 1<<20, 1, idxs(5, 10)...)
		}},
		{sCompact, "a compaction below the first retained index is nothing", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confCompact(t, l, 4, 0)
			if err := l.CompactPrefix(2); err != nil {
				t.Fatal(err)
			}
			wantBounds(t, l, 5, 10)
			wantTerm(t, l, 4, 1)
		}},
		{sUnsynced, "Entries and Bounds before any Sync", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 3, 1), nil)
			wantBounds(t, l, 1, 3)
			wantEntries(t, l, 1, 4, 1<<20, 1, 1, 2, 3)
			wantTerm(t, l, 3, 1)
		}},
		{sOneWriter, "readers concurrent with the writer read whole, ordered answers", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			var wg sync.WaitGroup
			stop := make(chan struct{})
			var bad atomic.Value
			for range 4 {
				wg.Go(func() {
					for {
						select {
						case <-stop:
							return
						default:
						}
						first, last := l.Bounds()
						if last < first {
							continue
						}
						got, err := l.Entries(first, last+1, 1<<20, nil)
						if err != nil {
							bad.Store(fmt.Sprintf("Entries(%d, %d): %v", first, last+1, err))
							return
						}
						for k, e := range got {
							if e.Position.Index != first+uint64(k) || !bytes.Equal(e.Data, confData(e.Position.Index, 1)) {
								bad.Store(fmt.Sprintf("entry %d of a read from %d is index %d %q", k, first, e.Position.Index, e.Data))
								return
							}
						}
					}
				})
			}
			for i := uint64(1); i <= 200; i++ {
				confAppend(t, l, confEntries(i, i, 1), nil)
				if i%50 == 0 {
					confSync(t, l)
				}
			}
			close(stop)
			wg.Wait()
			if v := bad.Load(); v != nil {
				t.Fatal(v)
			}
		}},
		{sAppend, "entries and the hard state of one Append are read together", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			hs := coreconsensus.HardState{Term: 2, Vote: 3, Commit: 1}
			confAppend(t, l, confEntries(1, 3, 2), &hs)
			if got := must(l.HardState())(t); got != hs {
				t.Fatalf("HardState() = %+v, want %+v", got, hs)
			}
			wantEntries(t, l, 1, 4, 1<<20, 2, 1, 2, 3)
		}},
		{sAppend, "a nil hs leaves the hard state", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			hs := coreconsensus.HardState{Term: 2, Vote: 3, Commit: 1}
			confAppend(t, l, confEntries(1, 3, 2), &hs)
			confAppend(t, l, confEntries(4, 5, 2), nil)
			if got := must(l.HardState())(t); got != hs {
				t.Fatalf("HardState() after an Append with no hard state = %+v, want %+v", got, hs)
			}
		}},
		{sAppend, "an entry out of sequence is refused STORAGE_FAILED", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 3, 1), nil)
			if err := l.Append(confEntries(5, 5, 1), nil); !errors.Is(err, coreconsensus.StorageFailed) {
				t.Fatalf("Append of index 5 after 3: %v, want STORAGE_FAILED", err)
			}
			wantBounds(t, l, 1, 3)
		}},
		{sHardState, "a log never written has the zero hard state", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			if got := must(l.HardState())(t); got != (coreconsensus.HardState{}) {
				t.Fatalf("HardState() of a new log = %+v", got)
			}
		}},
		{sHardState, "the later of two, alone in its Append", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 2, 1), &coreconsensus.HardState{Term: 1, Vote: 1})
			hs := coreconsensus.HardState{Term: 5, Vote: 2, Commit: 2}
			confAppend(t, l, nil, &hs)
			if got := must(l.HardState())(t); got != hs {
				t.Fatalf("HardState() = %+v, want %+v", got, hs)
			}
			wantBounds(t, l, 1, 2)
		}},
		{sTruncate, "Entries and Term end before from", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			confSync(t, l)
			if err := l.TruncateSuffix(6); err != nil {
				t.Fatal(err)
			}
			wantBounds(t, l, 1, 5)
			wantEntries(t, l, 1, 6, 1<<20, 1, idxs(1, 5)...)
			if _, err := l.Entries(1, 7, 1<<20, nil); err == nil {
				t.Fatal("Entries past the truncated end answered")
			}
			if _, err := l.Term(6); err == nil {
				t.Fatal("Term of a truncated index answered")
			}
		}},
		{sTruncate, "the next Append writes from on again", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 1), nil)
			if err := l.TruncateSuffix(6); err != nil {
				t.Fatal(err)
			}
			confAppend(t, l, confEntries(6, 8, 2), nil)
			wantTerm(t, l, 5, 1)
			wantTerm(t, l, 6, 2)
			wantEntries(t, l, 6, 9, 1<<20, 2, 6, 7, 8)
		}},
		{sTruncate, "from past the end removes nothing", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 4, 1), nil)
			if err := l.TruncateSuffix(9); err != nil {
				t.Fatal(err)
			}
			wantBounds(t, l, 1, 4)
		}},
		{sTruncate, "from below first keeps the compaction point", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 10, 3), nil)
			confCompact(t, l, 4, 0)
			if err := l.TruncateSuffix(2); err != nil {
				t.Fatal(err)
			}
			wantBounds(t, l, 5, 4)
			wantTerm(t, l, 4, 3)
		}},
		{sStamp, "a log never stamped has the zero stamp", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			if got := must(l.Stamp())(t); got != (coreconsensus.StampValue{}) {
				t.Fatalf("Stamp() of a new directory = %+v", got)
			}
		}},
		{sWriteStamp, "Stamp reads what WriteStamp wrote", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			if err := l.WriteStamp(confStamp); err != nil {
				t.Fatal(err)
			}
			if got := must(l.Stamp())(t); got != confStamp {
				t.Fatalf("Stamp() = %+v, want %+v", got, confStamp)
			}
		}},
		{sSync, "a Sync changes nothing a reader sees", false, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			confAppend(t, l, confEntries(1, 5, 1), &coreconsensus.HardState{Term: 1})
			confSync(t, l)
			confSync(t, l)
			wantBounds(t, l, 1, 5)
			wantEntries(t, l, 1, 6, 1<<20, 1, idxs(1, 5)...)
		}},
		{sSync, "a synced entry no longer depends on the buffer it was appended from", true, func(t *testing.T, l coreconsensus.LogStore, _ reopenFn) {
			es := confEntries(1, 3, 1)
			confAppend(t, l, es, nil)
			confSync(t, l)
			for _, e := range es {
				clear(e.Data)
			}
			wantEntries(t, l, 1, 4, 1<<20, 1, 1, 2, 3)
		}},
		{sPowerLoss, "synced entries and hard state survive a reopen", true, func(t *testing.T, l coreconsensus.LogStore, reopen reopenFn) {
			hs := coreconsensus.HardState{Term: 2, Vote: 1, Commit: 3}
			confAppend(t, l, confEntries(1, 6, 2), &hs)
			confSync(t, l)
			l = reopen(t)
			wantBounds(t, l, 1, 6)
			wantEntries(t, l, 1, 7, 1<<20, 2, idxs(1, 6)...)
			if got := must(l.HardState())(t); got != hs {
				t.Fatalf("HardState() after reopen = %+v, want %+v", got, hs)
			}
		}},
		{sTruncate, "a synced truncation survives a reopen", true, func(t *testing.T, l coreconsensus.LogStore, reopen reopenFn) {
			confAppend(t, l, confEntries(1, 8, 1), nil)
			confSync(t, l)
			if err := l.TruncateSuffix(4); err != nil {
				t.Fatal(err)
			}
			confSync(t, l)
			l = reopen(t)
			wantBounds(t, l, 1, 3)
		}},
		{sBoundsEmpty, "a compaction past the end survives a reopen", true, func(t *testing.T, l coreconsensus.LogStore, reopen reopenFn) {
			confAppend(t, l, confEntries(1, 5, 2), nil)
			confCompact(t, l, 30, 4)
			confSync(t, l)
			l = reopen(t)
			wantBounds(t, l, 31, 30)
			wantTerm(t, l, 30, 4)
		}},
		{sWriteStamp, "the stamp is read again after a reopen", true, func(t *testing.T, l coreconsensus.LogStore, reopen reopenFn) {
			if err := l.WriteStamp(confStamp); err != nil {
				t.Fatal(err)
			}
			l = reopen(t)
			if got := must(l.Stamp())(t); got != confStamp {
				t.Fatalf("Stamp() after reopen = %+v, want %+v", got, confStamp)
			}
		}},
	}
}

func rowName(sentence, detail string) string { return sentence + " — " + detail }

func TestConformanceLog(t *testing.T) {
	for _, eng := range everyLog() {
		t.Run(eng.name, func(t *testing.T) {
			for _, r := range logRows() {
				t.Run(rowName(r.sentence, r.detail), func(t *testing.T) {
					l, reopen := eng.open(t)
					if r.durable && reopen == nil {
						skipUndurable(t, r.sentence, eng.name)
					}
					r.run(t, l, reopen)
				})
			}
		})
	}
}

// ---- SnapshotStore ----

// snapEngine is one implementation of SnapshotStore under test. reopen,
// nil for an engine with no durability, opens a new store over the same
// state.
type snapEngine struct {
	name string
	open func(t *testing.T) (coreconsensus.SnapshotStore, func(t *testing.T) coreconsensus.SnapshotStore)
}

// everySnapshotStore is every SnapshotStore the package ships: the memory
// double and the file store over a temporary directory.
func everySnapshotStore() []snapEngine {
	return []snapEngine{
		{name: "memory", open: func(*testing.T) (coreconsensus.SnapshotStore, func(*testing.T) coreconsensus.SnapshotStore) {
			return NewMemorySnapshots(), nil
		}},
		{name: "file", open: func(t *testing.T) (coreconsensus.SnapshotStore, func(*testing.T) coreconsensus.SnapshotStore) {
			dir := t.TempDir()
			openStore := func(t *testing.T) coreconsensus.SnapshotStore {
				t.Helper()
				s, err := newFileSnapshots(dir, storageFailed)
				if err != nil {
					t.Fatalf("newFileSnapshots: %v %v", err, errs.FieldsOf(err))
				}
				return s
			}
			return openStore(t), openStore
		}},
	}
}

type snapRow struct {
	sentence string
	detail   string
	durable  bool
	run      func(t *testing.T, s coreconsensus.SnapshotStore, reopen func(*testing.T) coreconsensus.SnapshotStore)
}

// confChunkSize is the decoded chunk size of every snapshot of the table.
const confChunkSize = 64

// confChunk is one encoded chunk of the stream format (snapcodec.go) over
// p, its bytes stored as they are: the stores check the header and the
// CRC-32C, never the codec.
func confChunk(p []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(p)))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(p)))
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(p, castagnoli))
	return append(b, p...)
}

// confStream is n chunks, chunk k of confChunkSize bytes of k+1.
func confStream(n int) []byte {
	var b []byte
	for k := range n {
		b = append(b, confChunk(bytes.Repeat([]byte{byte(k + 1)}, confChunkSize))...)
	}
	return b
}

// confMeta is the metadata of a snapshot id at index whose encoded stream
// is size bytes.
func confMeta(id string, index uint64, size int64) coreconsensus.SnapshotMetaValue {
	return coreconsensus.SnapshotMetaValue{
		ID: id, Version: 1, Position: coreconsensus.PositionValue{Term: 1, Index: index},
		Membership: coreconsensus.MembershipValue{Index: index}, Codec: "none",
		EncodedSize: size, DecodedSize: size, ChunkSize: confChunkSize,
	}
}

// confPublish spools and commits snapshot id at index over n chunks.
func confPublish(t *testing.T, s coreconsensus.SnapshotStore, id string, index uint64, n int) []byte {
	t.Helper()
	body := confStream(n)
	meta := confMeta(id, index, int64(len(body)))
	sink, off, err := s.Create(id, meta)
	if err != nil || off != 0 {
		t.Fatalf("Create(%s) = %d %v", id, off, err)
	}
	if _, err := sink.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := sink.Commit(meta); err != nil {
		t.Fatalf("Commit(%s): %v %v", id, err, errs.FieldsOf(err))
	}
	return body
}

func listedIDs(t *testing.T, s coreconsensus.SnapshotStore) []string {
	t.Helper()
	ms, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

func wantListed(t *testing.T, s coreconsensus.SnapshotStore, want ...string) {
	t.Helper()
	if got := listedIDs(t, s); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("List() = %v, want %v", got, want)
	}
}

func mustOpenSnap(t *testing.T, s coreconsensus.SnapshotStore, id string) coreconsensus.SnapshotHandle {
	t.Helper()
	h, err := s.Open(id)
	if err != nil {
		t.Fatalf("Open(%s): %v %v", id, err, errs.FieldsOf(err))
	}
	return h
}

// spoolTorn writes two whole chunks and the first half of a third to a
// new spool of id, and releases its sink without a Commit, as a broken
// transfer leaves it.
func spoolTorn(t *testing.T, s coreconsensus.SnapshotStore, id string) (whole int64, meta coreconsensus.SnapshotMetaValue) {
	t.Helper()
	body := confStream(3)
	meta = confMeta(id, 10, int64(len(body)))
	sink, off, err := s.Create(id, meta)
	if err != nil || off != 0 {
		t.Fatalf("Create = %d %v", off, err)
	}
	whole = int64(len(confStream(2)))
	if _, err := sink.Write(body[:whole+int64(len(confChunk(nil)))+confChunkSize/2]); err != nil {
		t.Fatal(err)
	}
	releaseSink(sink)
	return whole, meta
}

const (
	sCreate    = "Create opens the spool of id."
	sResume    = "When a spool of that id exists, it is returned with the ENCODED offset durably spooled so far."
	sCommit    = "Commit publishes the snapshot"
	sAbort     = "Abort drops the spool."
	sList      = "List returns the complete snapshots, newest first."
	sOpen      = "Open pins a snapshot until the handle is closed."
	sRemove    = "Remove deletes a snapshot; refused while a handle is open."
	sMeta      = "Meta describes the snapshot."
	sHandle    = "SnapshotHandle is a published snapshot, pinned until Close."
	sHoldsSnap = "SnapshotStore holds snapshots."
)

func snapRows() []snapRow {
	type reopenFn = func(*testing.T) coreconsensus.SnapshotStore
	return []snapRow{
		{sCreate, "a new id spools from offset 0", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			sink, off, err := s.Create("snap-1", confMeta("snap-1", 1, 1))
			if err != nil || off != 0 {
				t.Fatalf("Create = %d %v", off, err)
			}
			abandon(sink.Abort())
		}},
		{sResume, "a resume starts after the last whole chunk", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			whole, meta := spoolTorn(t, s, "snap-10")
			sink, off, err := s.Create("snap-10", meta)
			if err != nil || off != whole {
				t.Fatalf("Create over a spool of two whole chunks and a torn third = %d %v, want %d", off, err, whole)
			}
			abandon(sink.Abort())
		}},
		{sResume, "the stream resumed from the offset commits whole", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			whole, meta := spoolTorn(t, s, "snap-10")
			sink, off, err := s.Create("snap-10", meta)
			if err != nil || off != whole {
				t.Fatalf("Create = %d %v, want %d", off, err, whole)
			}
			body := confStream(3)
			if _, err := sink.Write(body[off:]); err != nil {
				t.Fatal(err)
			}
			if err := sink.Commit(meta); err != nil {
				t.Fatalf("Commit: %v %v", err, errs.FieldsOf(err))
			}
			h := mustOpenSnap(t, s, "snap-10")
			defer h.Close()
			got := make([]byte, len(body))
			if _, err := h.ReadAt(got, 0); err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body) {
				t.Fatal("the resumed snapshot does not read back as the stream it was spooled from")
			}
		}},
		{sCommit, "nothing is listed before the Commit", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confStream(2)
			sink, _, err := s.Create("snap-5", confMeta("snap-5", 5, int64(len(body))))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sink.Write(body); err != nil {
				t.Fatal(err)
			}
			wantListed(t, s)
			if _, err := s.Open("snap-5"); err == nil {
				t.Fatal("an uncommitted spool opened as a snapshot")
			}
			abandon(sink.Abort())
		}},
		{sCommit, "a Commit lists and opens the snapshot", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			confPublish(t, s, "snap-5", 5, 2)
			wantListed(t, s, "snap-5")
			h := mustOpenSnap(t, s, "snap-5")
			abandon(h.Close())
		}},
		{sCommit, "a Commit whose encoded size is not the spool's is SNAPSHOT_CORRUPT and publishes nothing", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confStream(2)
			meta := confMeta("snap-5", 5, int64(len(body)))
			sink, _, err := s.Create("snap-5", meta)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sink.Write(body); err != nil {
				t.Fatal(err)
			}
			meta.EncodedSize++
			if err := sink.Commit(meta); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
				t.Fatalf("Commit of a short spool: %v, want SNAPSHOT_CORRUPT", err)
			}
			wantListed(t, s)
		}},
		{sAbort, "nothing is listed, and the id spools from 0 again", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confStream(2)
			meta := confMeta("snap-5", 5, int64(len(body)))
			sink, _, err := s.Create("snap-5", meta)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sink.Write(body); err != nil {
				t.Fatal(err)
			}
			if err := sink.Abort(); err != nil {
				t.Fatal(err)
			}
			wantListed(t, s)
			again, off, err := s.Create("snap-5", meta)
			if err != nil || off != 0 {
				t.Fatalf("Create after an Abort = %d %v, want 0", off, err)
			}
			abandon(again.Abort())
		}},
		{sList, "three snapshots, newest first whatever their order of commit", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			confPublish(t, s, "snap-5", 5, 1)
			confPublish(t, s, "snap-20", 20, 1)
			confPublish(t, s, "snap-10", 10, 1)
			wantListed(t, s, "snap-20", "snap-10", "snap-5")
		}},
		{sList, "the listed metadata is the committed one", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confPublish(t, s, "snap-7", 7, 2)
			ms, err := s.List()
			if err != nil || len(ms) != 1 {
				t.Fatalf("List() = %v %v", ms, err)
			}
			if ms[0].ID != "snap-7" || ms[0].Position != (coreconsensus.PositionValue{Term: 1, Index: 7}) || ms[0].EncodedSize != int64(len(body)) {
				t.Fatalf("listed metadata %+v", ms[0])
			}
		}},
		{sRemove, "Remove of an open handle is refused, and the snapshot stays", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			confPublish(t, s, "snap-5", 5, 1)
			h := mustOpenSnap(t, s, "snap-5")
			err := s.Remove("snap-5")
			if !errors.Is(err, coreconsensus.AdminPreconditionFailed) || errors.Is(err, coreconsensus.StorageFailed) {
				t.Fatalf("Remove of a pinned snapshot: %v, want ADMIN_PRECONDITION_FAILED", err)
			}
			wantListed(t, s, "snap-5")
			abandon(h.Close())
		}},
		{sOpen, "the pin ends at Close, and Remove then deletes", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			confPublish(t, s, "snap-5", 5, 1)
			h := mustOpenSnap(t, s, "snap-5")
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			if err := s.Remove("snap-5"); err != nil {
				t.Fatalf("Remove after Close: %v", err)
			}
			wantListed(t, s)
			if _, err := s.Open("snap-5"); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
				t.Fatalf("Open of a removed snapshot: %v, want SNAPSHOT_CORRUPT", err)
			}
		}},
		{sOpen, "every handle pins, and a second Close releases nothing", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			confPublish(t, s, "snap-5", 5, 1)
			h1, h2 := mustOpenSnap(t, s, "snap-5"), mustOpenSnap(t, s, "snap-5")
			abandon(h1.Close())
			abandon(h1.Close())
			if err := s.Remove("snap-5"); err == nil {
				t.Fatal("Remove went through while a second handle was open")
			}
			abandon(h2.Close())
			if err := s.Remove("snap-5"); err != nil {
				t.Fatalf("Remove once every handle closed: %v", err)
			}
		}},
		{sOpen, "an id never committed is SNAPSHOT_CORRUPT", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			if _, err := s.Open("snap-404"); !errors.Is(err, coreconsensus.SnapshotCorrupt) {
				t.Fatalf("Open of an unknown id: %v, want SNAPSHOT_CORRUPT", err)
			}
		}},
		{sRemove, "an id that holds nothing is removed without a word", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			if err := s.Remove("snap-404"); err != nil {
				t.Fatalf("Remove of an unknown id: %v", err)
			}
		}},
		{sHandle, "the handle reads the committed stream at any offset", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confPublish(t, s, "snap-5", 5, 3)
			h := mustOpenSnap(t, s, "snap-5")
			defer h.Close()
			got := make([]byte, 40)
			if _, err := h.ReadAt(got, 30); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body[30:70]) {
				t.Fatal("ReadAt(30) does not read the committed bytes")
			}
		}},
		{sMeta, "the handle's metadata is the committed one", false, func(t *testing.T, s coreconsensus.SnapshotStore, _ reopenFn) {
			body := confPublish(t, s, "snap-5", 5, 2)
			h := mustOpenSnap(t, s, "snap-5")
			defer h.Close()
			if m := h.Meta(); m.ID != "snap-5" || m.Position.Index != 5 || m.EncodedSize != int64(len(body)) || m.ChunkSize != confChunkSize {
				t.Fatalf("Meta() = %+v", m)
			}
		}},
		{sHoldsSnap, "a committed snapshot is listed and read by a store opened again", true, func(t *testing.T, s coreconsensus.SnapshotStore, reopen reopenFn) {
			body := confPublish(t, s, "snap-5", 5, 2)
			s = reopen(t)
			wantListed(t, s, "snap-5")
			h := mustOpenSnap(t, s, "snap-5")
			defer h.Close()
			got := make([]byte, len(body))
			if _, err := h.ReadAt(got, 0); err != nil && !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			if !bytes.Equal(got, body) {
				t.Fatal("the reopened snapshot does not read back as committed")
			}
		}},
		{sResume, "a store opened again resumes the newest spool", true, func(t *testing.T, s coreconsensus.SnapshotStore, reopen reopenFn) {
			whole, meta := spoolTorn(t, s, "snap-10")
			s = reopen(t)
			sink, off, err := s.Create("snap-10", meta)
			if err != nil || off != whole {
				t.Fatalf("Create on a reopened store = %d %v, want %d", off, err, whole)
			}
			abandon(sink.Abort())
		}},
	}
}

func TestConformanceSnapshotStore(t *testing.T) {
	for _, eng := range everySnapshotStore() {
		t.Run(eng.name, func(t *testing.T) {
			for _, r := range snapRows() {
				t.Run(rowName(r.sentence, r.detail), func(t *testing.T) {
					s, reopen := eng.open(t)
					if r.durable && reopen == nil {
						skipUndurable(t, r.sentence, eng.name)
					}
					r.run(t, s, reopen)
				})
			}
		})
	}
}

// ---- Transport ----

// confPair is a sender a and a receiver b of one engine, each admitting
// the other in group 1.
type confPair struct {
	a, b    coreconsensus.Transport
	aID     coreconsensus.NodeID
	bID     coreconsensus.NodeID
	members []coreconsensus.MemberValue
	// oversized reads how many frames a dropped for being above its peer
	// queue; nil for an engine that bounds no queue in bytes.
	oversized func() int64
}

// confPairOpts bounds the pair: recvFrame the offer b reads, queue and
// frame a's peer queue and frame bound; 0 is the engine's default.
type confPairOpts struct {
	recvFrame int
	frame     int
	queue     int64
}

type transportEngine struct {
	name string
	pair func(t *testing.T, o confPairOpts) *confPair
}

// everyTransport is every Transport the package ships: a pair of the
// memory network, its admission model on so SetMembership is held as the
// TCP transport holds it, and two TCP transports over a test CA on
// loopback.
func everyTransport() []transportEngine {
	return []transportEngine{
		{name: "memory", pair: func(t *testing.T, o confPairOpts) *confPair {
			n := NewMemoryNetwork()
			n.admission = true
			a, b := NewMemoryTransport(n, 1), NewMemoryTransport(n, 2)
			b.maxOffer = o.recvFrame
			t.Cleanup(func() {
				abandon(a.Close())
				abandon(b.Close())
			})
			p := &confPair{a: a, b: b, aID: 1, bID: 2, members: []coreconsensus.MemberValue{
				{ID: 1, Slot: "site-a", SPKI: memSPKI(1), Voter: true},
				{ID: 2, Slot: "site-b", SPKI: memSPKI(2), Voter: true},
			}}
			a.SetMembership(1, p.members)
			b.SetMembership(1, p.members)
			return p
		}},
		{name: "tcp", pair: func(t *testing.T, o confPairOpts) *confPair {
			ca := newTestCA(t)
			cid := coreconsensus.ClusterID{0x11, 0x14}
			open := func(slot string, frame int, queue int64) (*TCPTransport, [32]byte) {
				cert, spki := ca.issue(t, SAN(cid, slot), nil)
				tr, err := NewTCPTransport(&TransportConfig{Certificate: cert, Roots: ca.pool, ListenAddress: "127.0.0.1:0", MaxFrameBytes: frame, PeerQueueBytes: queue})
				if err != nil {
					t.Fatalf("NewTCPTransport: %v %v", err, errs.FieldsOf(err))
				}
				t.Cleanup(func() { abandon(tr.Close()) })
				return tr, spki
			}
			a, spkiA := open("site-a", o.frame, o.queue)
			b, spkiB := open("site-b", o.recvFrame, 0)
			p := &confPair{a: a, b: b, aID: 1, bID: 2, members: []coreconsensus.MemberValue{
				{ID: 1, Slot: "site-a", SPKI: spkiA, Address: a.Addr(), Voter: true},
				{ID: 2, Slot: "site-b", SPKI: spkiB, Address: b.Addr(), Voter: true},
			}, oversized: a.oversized.Load}
			a.SetMembership(1, p.members)
			b.SetMembership(1, p.members)
			return p
		}},
	}
}

// confDelivery is one frame b's deliver got, copied: the frame is valid
// only until deliver returns.
type confDelivery struct {
	from  coreconsensus.NodeID
	frame []byte
}

// bindReceiver binds b with a deliver that records every frame, and the
// offer and install given, nil for none.
func (p *confPair) bindReceiver(t *testing.T,
	offer func(coreconsensus.AuthenticatedPeerValue, coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue,
	install func(coreconsensus.InstallParams) error,
) chan confDelivery {
	t.Helper()
	got := make(chan confDelivery, 256)
	deliver := func(from coreconsensus.AuthenticatedPeerValue, frame []byte) {
		got <- confDelivery{from: from.ID, frame: bytes.Clone(frame)}
	}
	if err := p.b.Bind(1, deliver, offer, install, nil); err != nil {
		t.Fatal(err)
	}
	return got
}

// confFrame is a frame from a to b of kind at term, padded to at least
// size bytes: a transport routes it by its frozen header alone, and a
// padded frame is only ever sent to be dropped.
func (p *confPair) confFrame(kind coreconsensus.MessageKind, term uint64, size int) []byte {
	f := appendFrame(&coreconsensus.MessageValue{Version: 1, Group: 1, Kind: kind, From: p.aID, To: p.bID, Term: term})
	if len(f) < size {
		f = append(f, make([]byte, size-len(f))...)
	}
	return f
}

// confNext is the next delivery within wait, false when none came.
func confNext(got chan confDelivery, wait time.Duration) (confDelivery, bool) {
	select {
	case d := <-got:
		return d, true
	case <-time.After(wait):
		return confDelivery{}, false
	}
}

// sendUntil sends frame from a to b until one reaches b, within 5 s: a
// TCP link is dialled by the first frame and a frame sent while a
// membership change settles may be dropped, as the port allows.
func (p *confPair) sendUntil(t *testing.T, got chan confDelivery, frame []byte) confDelivery {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		p.a.Send(p.bID, bytes.Clone(frame))
		if d, ok := confNext(got, 100*time.Millisecond); ok {
			return d
		}
	}
	t.Fatal("no frame reached the receiver within 5 s")
	return confDelivery{}
}

// wantSilence sends frame a few times and requires that none reaches b.
func (p *confPair) wantSilence(t *testing.T, got chan confDelivery, frame []byte, why string) {
	t.Helper()
	for range 5 {
		p.a.Send(p.bID, bytes.Clone(frame))
		if d, ok := confNext(got, 100*time.Millisecond); ok {
			t.Fatalf("%s: a frame from %d reached the receiver", why, d.from)
		}
	}
}

// confOffer is an offer of a snapshot of body.
func confOffer(body []byte) coreconsensus.InstallOfferValue {
	meta := confMeta("snap-9", 9, int64(len(body)))
	return coreconsensus.InstallOfferValue{Term: 1, SnapshotID: meta.ID, Meta: meta}
}

// confCloser records its Close.
type confCloser struct {
	io.Reader
	closed *atomic.Int32
}

func (c confCloser) Close() error {
	c.closed.Add(1)
	return nil
}

type transportRow struct {
	sentence string
	detail   string
	opts     confPairOpts
	run      func(t *testing.T, p *confPair)
}

const (
	sSend        = "Send hands over an encoded frame."
	sNeverBlocks = "It never blocks and may drop"
	sDeliver     = "deliver gets the frame and the peer TLS authenticated"
	sStream      = "Stream sends a snapshot from the offset the receiver accepted"
	sStreamClose = "open returns a reader at an encoded offset on a chunk boundary, which Stream closes."
	sMembership  = "SetMembership pushes the group's admitted members"
	sUnion       = "the transport computes the union over its groups."
	sUnreachable = "Unreachable registers the callback."
)

func transportRows() []transportRow {
	streamBody := confStream(3)
	return []transportRow{
		{sSend, "a control-lane frame arrives intact", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			f := p.confFrame(coreconsensus.MsgPreVote, 2, 0)
			if d := p.sendUntil(t, got, f); !bytes.Equal(d.frame, f) {
				t.Fatalf("delivered %x, sent %x", d.frame, f)
			}
		}},
		{sSend, "a data-lane frame arrives intact", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			f := p.confFrame(coreconsensus.MsgApp, 2, 0)
			if d := p.sendUntil(t, got, f); !bytes.Equal(d.frame, f) {
				t.Fatalf("delivered %x, sent %x", d.frame, f)
			}
		}},
		{sSend, "the frames of one lane arrive in the order sent", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			// The frames of term 1 sendUntil sent may still arrive: the
			// order is held from term 2 on, each sent once.
			p.sendUntil(t, got, p.confFrame(coreconsensus.MsgApp, 1, 0))
			for term := uint64(2); term <= 40; term++ {
				p.a.Send(p.bID, p.confFrame(coreconsensus.MsgApp, term, 0))
			}
			var last uint64 = 1
			for {
				d, ok := confNext(got, 500*time.Millisecond)
				if !ok {
					break
				}
				var m coreconsensus.MessageValue
				if err := wire.ParseMessage(d.frame, &m); err != nil {
					t.Fatal(err)
				}
				if m.Term < last {
					t.Fatalf("term %d delivered after term %d on one lane", m.Term, last)
				}
				last = m.Term
			}
			if last != 40 {
				t.Fatalf("the last frame delivered is of term %d, want 40", last)
			}
		}},
		{sDeliver, "the frame comes with its sender's ID", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			if d := p.sendUntil(t, got, p.confFrame(coreconsensus.MsgHeartbeat, 1, 0)); d.from != p.aID {
				t.Fatalf("delivered from %d, sent by %d", d.from, p.aID)
			}
		}},
		{sNeverBlocks, "a frame to a node outside the membership returns at once and reaches no one", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			start := time.Now()
			for range 100 {
				p.a.Send(99, p.confFrame(coreconsensus.MsgApp, 1, 0))
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("100 Sends to an unknown node took %v", d)
			}
			if d, ok := confNext(got, 300*time.Millisecond); ok {
				t.Fatalf("a frame to node 99 was delivered to %d", d.from)
			}
		}},
		{sNeverBlocks, "a frame above PeerQueueBytes is dropped and counted", confPairOpts{frame: 256 << 10, queue: 256 << 10}, func(t *testing.T, p *confPair) {
			if p.oversized == nil {
				t.Skipf("%q: the double bounds each lane by %d frames, not by bytes, and counts no drop", sNeverBlocks, memQueueFrames)
			}
			got := p.bindReceiver(t, nil, nil)
			p.a.Send(p.bID, p.confFrame(coreconsensus.MsgApp, 1, 256<<10+1))
			if n := p.oversized(); n != 1 {
				t.Fatalf("dropped-oversized count %d after one frame above the queue, want 1", n)
			}
			if d := p.sendUntil(t, got, p.confFrame(coreconsensus.MsgApp, 2, 0)); len(d.frame) > 256<<10 {
				t.Fatal("the frame above the queue was delivered")
			}
		}},
		{sStream, "open is called at the accepted offset and the install reads from there", confPairOpts{}, func(t *testing.T, p *confPair) {
			const at = 76
			var installed []byte
			var offset int64 = -1
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID, Offset: at}
			}, func(ip coreconsensus.InstallParams) error {
				offset = ip.Offset
				var err error
				installed, err = io.ReadAll(ip.Body)
				return err
			})
			var opened int64 = -1
			open := func(off int64) (io.ReadCloser, error) {
				opened = off
				return io.NopCloser(bytes.NewReader(streamBody[off:])), nil
			}
			if err := confStreamTo(p, confOffer(streamBody), open); err != nil {
				t.Fatalf("Stream: %v %v", err, errs.FieldsOf(err))
			}
			if opened != at || offset != at || !bytes.Equal(installed, streamBody[at:]) {
				t.Fatalf("opened at %d, installed at %d with %d bytes; want %d and %d bytes", opened, offset, len(installed), at, len(streamBody)-at)
			}
		}},
		{sStreamClose, "the reader open returned is closed once", confPairOpts{}, func(t *testing.T, p *confPair) {
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID}
			}, func(ip coreconsensus.InstallParams) error {
				_, err := io.Copy(io.Discard, ip.Body)
				return err
			})
			var closed atomic.Int32
			open := func(off int64) (io.ReadCloser, error) {
				return confCloser{Reader: bytes.NewReader(streamBody[off:]), closed: &closed}, nil
			}
			if err := confStreamTo(p, confOffer(streamBody), open); err != nil {
				t.Fatalf("Stream: %v", err)
			}
			if n := closed.Load(); n != 1 {
				t.Fatalf("the stream's reader was closed %d times, want 1", n)
			}
		}},
		{sStream, "a refused offer is the stream's error, and nothing is opened", confPairOpts{}, func(t *testing.T, p *confPair) {
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID, Refused: coreconsensus.CodeOverloaded}
			}, func(coreconsensus.InstallParams) error { return nil })
			open := func(int64) (io.ReadCloser, error) {
				t.Error("a refused offer opened its stream")
				return io.NopCloser(bytes.NewReader(nil)), nil
			}
			if err := confStreamTo(p, confOffer(streamBody), open); !errors.Is(err, coreconsensus.Overloaded) {
				t.Fatalf("Stream of a refused offer: %v, want OVERLOADED", err)
			}
		}},
		{sStream, "an offer the receiver holds already succeeds with nothing streamed", confPairOpts{}, func(t *testing.T, p *confPair) {
			var installs atomic.Int32
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID, Held: true}
			}, func(coreconsensus.InstallParams) error {
				installs.Add(1)
				return nil
			})
			open := func(int64) (io.ReadCloser, error) {
				t.Error("a held offer opened its stream")
				return io.NopCloser(bytes.NewReader(nil)), nil
			}
			if err := confStreamTo(p, confOffer(streamBody), open); err != nil || installs.Load() != 0 {
				t.Fatalf("Stream of a held offer: %v, %d installs", err, installs.Load())
			}
		}},
		{sStream, "the install's refusal is the stream's error", confPairOpts{}, func(t *testing.T, p *confPair) {
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID}
			}, func(ip coreconsensus.InstallParams) error {
				_, _ = io.Copy(io.Discard, ip.Body)
				return origin(coreconsensus.NoSpace)
			})
			open := func(off int64) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(streamBody[off:])), nil
			}
			if err := confStreamTo(p, confOffer(streamBody), open); !errors.Is(err, coreconsensus.NoSpace) {
				t.Fatalf("Stream of an install refused NO_SPACE: %v", err)
			}
		}},
		{sStream, "an offer above the receiver's frame bound is refused before it is judged", confPairOpts{recvFrame: 256 << 10}, func(t *testing.T, p *confPair) {
			var judged atomic.Int32
			p.bindReceiver(t, func(_ coreconsensus.AuthenticatedPeerValue, o coreconsensus.InstallOfferValue) coreconsensus.InstallAcceptValue {
				judged.Add(1)
				return coreconsensus.InstallAcceptValue{SnapshotID: o.SnapshotID}
			}, func(ip coreconsensus.InstallParams) error {
				_, err := io.Copy(io.Discard, ip.Body)
				return err
			})
			// The sender sees its stream end unanswered: FRAME_INVALID on the
			// double, the connection's reset or a short accept over TCP.
			o := retiredOffer(nil, 40000) // 320 000 bytes of retired IDs
			if err := confStreamTo(p, o, retiredBody); err == nil || judged.Load() != 0 {
				t.Fatalf("an offer of %d bytes above a bound of %d: %v, judged %d times", len(encodeOffer(o)), 256<<10, err, judged.Load())
			}
		}},
		{sMembership, "a peer the receiver no longer admits is no longer delivered", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			f := p.confFrame(coreconsensus.MsgHeartbeat, 1, 0)
			p.sendUntil(t, got, f)
			p.b.SetMembership(1, p.members[1:])
			time.Sleep(100 * time.Millisecond)
			drain(got)
			p.wantSilence(t, got, f, "the receiver dropped the sender from its membership")
			p.b.SetMembership(1, p.members)
			p.sendUntil(t, got, f)
		}},
		{sMembership, "the sender sends nothing to a peer it no longer admits", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			f := p.confFrame(coreconsensus.MsgHeartbeat, 1, 0)
			p.sendUntil(t, got, f)
			p.a.SetMembership(1, p.members[:1])
			time.Sleep(100 * time.Millisecond)
			drain(got)
			p.wantSilence(t, got, f, "the sender dropped the receiver from its membership")
		}},
		{sUnion, "a peer admitted in another group is still delivered", confPairOpts{}, func(t *testing.T, p *confPair) {
			got := p.bindReceiver(t, nil, nil)
			f := p.confFrame(coreconsensus.MsgHeartbeat, 1, 0)
			p.b.SetMembership(2, p.members)
			p.b.SetMembership(1, p.members[1:])
			p.sendUntil(t, got, f)
			p.b.SetMembership(2, nil)
			time.Sleep(100 * time.Millisecond)
			drain(got)
			p.wantSilence(t, got, f, "the sender left the last group that admitted it")
		}},
		{sUnreachable, "a peer that cannot be dialled is reported", confPairOpts{}, func(t *testing.T, p *confPair) {
			r, ok := p.a.(coreconsensus.UnreachableReporter)
			if !ok {
				t.Skipf("%q: the double is no UnreachableReporter; a cut link drops its frames without a word", sUnreachable)
			}
			reported := make(chan coreconsensus.NodeID, 16)
			r.Unreachable(func(id coreconsensus.NodeID) {
				select {
				case reported <- id:
				default:
				}
			})
			if c, ok := p.b.(io.Closer); ok {
				abandon(c.Close())
			}
			p.a.Send(p.bID, p.confFrame(coreconsensus.MsgHeartbeat, 1, 0))
			select {
			case id := <-reported:
				if id != p.bID {
					t.Fatalf("reported %d unreachable, want %d", id, p.bID)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("a peer whose listener is closed was never reported unreachable")
			}
		}},
	}
}

// confStreamTo streams o from a to b under a 5 s bound.
func confStreamTo(p *confPair, o coreconsensus.InstallOfferValue, open func(int64) (io.ReadCloser, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return p.a.Stream(ctx, p.bID, o, open)
}

// drain empties got of what was already delivered.
func drain(got chan confDelivery) {
	for {
		select {
		case <-got:
		default:
			return
		}
	}
}

func TestConformanceTransport(t *testing.T) {
	for _, eng := range everyTransport() {
		t.Run(eng.name, func(t *testing.T) {
			for _, r := range transportRows() {
				t.Run(rowName(r.sentence, r.detail), func(t *testing.T) {
					r.run(t, eng.pair(t, r.opts))
				})
			}
		})
	}
}

// ---- the sentences ----

// portsDoc is the doc text of every comment of gen_ports.go, each comment
// group joined on one line.
func portsDoc(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "..", "..", "core", "app", "consensus", "gen_ports.go")
	if srcdir, wks := os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"); srcdir != "" && wks != "" {
		// Under Bazel the file is a runfile of //internal/core/app/consensus:audit_srcs.
		path = filepath.Join(srcdir, wks, filepath.FromSlash(portsSource))
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", portsSource, err)
	}
	var docs []string
	for _, g := range f.Comments {
		docs = append(docs, strings.Join(strings.Fields(g.Text()), " "))
	}
	return docs
}

// Every row of the three tables quotes a sentence of its port's doc, and
// each table holds the rows #1114 asked for on every engine.
func TestConformanceRowsQuoteThePorts(t *testing.T) {
	docs := portsDoc(t)
	quoted := func(s string) bool {
		for _, d := range docs {
			if strings.Contains(d, s) {
				return true
			}
		}
		return false
	}
	var sentences []string
	logs, snaps, transports := logRows(), snapRows(), transportRows()
	everyEngine := func(durable []bool) int {
		n := 0
		for _, d := range durable {
			if !d {
				n++
			}
		}
		return n
	}
	var logDurable, snapDurable []bool
	for _, r := range logs {
		sentences = append(sentences, r.sentence)
		logDurable = append(logDurable, r.durable)
	}
	for _, r := range snaps {
		sentences = append(sentences, r.sentence)
		snapDurable = append(snapDurable, r.durable)
	}
	for _, r := range transports {
		sentences = append(sentences, r.sentence)
	}
	for _, s := range sentences {
		if !quoted(s) {
			t.Errorf("a row quotes %q, which %s does not say", s, portsSource)
		}
	}
	if n := everyEngine(logDurable); n < 20 {
		t.Errorf("%d log rows run on every engine, want at least 20", n)
	}
	if n := everyEngine(snapDurable); n < 10 {
		t.Errorf("%d snapshot store rows run on every engine, want at least 10", n)
	}
	// Two transport rows skip themselves on the double, by what it is.
	if n := len(transports) - 2; n < 8 {
		t.Errorf("%d transport rows run on every engine, want at least 8", n)
	}
}
