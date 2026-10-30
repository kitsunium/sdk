package consensus

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Diagnostic for #768, never merged: what a listing and a publication or a
// removal of the same store, on two goroutines, do to each other on this
// kernel. Everything is in t.TempDir().

type diagTally struct {
	mu sync.Mutex
	n  map[string]int
}

func (d *diagTally) add(side string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.n == nil {
		d.n = map[string]int{}
	}
	d.n[side+": "+err.Error()+" "+fmt.Sprint(errs.FieldsOf(err))]++
}

func (d *diagTally) String() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for k, v := range d.n {
		out = append(out, fmt.Sprintf("%6d x %s", v, k))
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "none"
	}
	return "\n" + strings.Join(out, "\n")
}

func diagPublish(s *fileSnapshots, meta coreconsensus.SnapshotMetaValue) error {
	meta.ID, meta.EncodedSize = snapshotID(meta.Position.Index, meta.Position.Term), 1
	sink, _, err := s.Create(meta.ID, meta)
	if err != nil {
		return err
	}
	if _, err := sink.Write([]byte{0}); err != nil {
		return err
	}
	return sink.Commit(meta)
}

func diagMeta(idx uint64) coreconsensus.SnapshotMetaValue {
	return coreconsensus.SnapshotMetaValue{Version: 1, Position: coreconsensus.PositionValue{Index: idx, Term: 2}, ChunkSize: defaultChunkSize}
}

// diagRace runs write rounds times while a reader lists, with serial held by
// both around their calls when set.
func diagRace(t *testing.T, name string, rounds int, reader, serial bool, write func(s *fileSnapshots) error) {
	s, err := newFileSnapshots(t.TempDir(), storageFailed)
	if err != nil {
		t.Fatal(err)
	}
	for _, idx := range []uint64{20, 40} {
		if err := diagPublish(s, diagMeta(idx)); err != nil {
			t.Fatal(err)
		}
	}
	var tally diagTally
	var mu sync.Mutex
	stop := make(chan struct{})
	var wg sync.WaitGroup
	lists := 0
	if reader {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if serial {
					mu.Lock()
				}
				_, err := s.List()
				if serial {
					mu.Unlock()
				}
				lists++
				if err != nil {
					tally.add("list", err)
				}
			}
		})
	}
	for range rounds {
		if serial {
			mu.Lock()
		}
		err := write(s)
		if serial {
			mu.Unlock()
		}
		if err != nil {
			tally.add("write", err)
		}
	}
	close(stop)
	wg.Wait()
	t.Logf("diag768 %s: %d rounds, %d lists, errors: %s", name, rounds, lists, &tally)
}

func TestDiag768Republish(t *testing.T) {
	again := func(s *fileSnapshots) error { return diagPublish(s, diagMeta(40)) }
	diagRace(t, "republish, no reader", 1000, false, false, again)
	diagRace(t, "republish, a reader", 1000, true, false, again)
	diagRace(t, "republish, a reader, serialised", 1000, true, true, again)
}

func TestDiag768Remove(t *testing.T) {
	cycle := func(s *fileSnapshots) error {
		if err := diagPublish(s, diagMeta(60)); err != nil {
			return err
		}
		return s.Remove(snapshotID(60, 2))
	}
	diagRace(t, "publish+remove, no reader", 1000, false, false, cycle)
	diagRace(t, "publish+remove, a reader", 1000, true, false, cycle)
	diagRace(t, "publish+remove, a reader, serialised", 1000, true, true, cycle)
}

func TestDiag768Fresh(t *testing.T) {
	next := uint64(100)
	fresh := func(s *fileSnapshots) error {
		next++
		if err := diagPublish(s, diagMeta(next)); err != nil {
			return err
		}
		return s.Remove(snapshotID(next-1, 2))
	}
	diagRace(t, "fresh ids, a reader", 1000, true, false, fresh)
}
