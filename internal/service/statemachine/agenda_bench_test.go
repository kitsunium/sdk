// Package statemachine_test — what finding the next transition due costs:
// the agenda against the sweep it replaces, over 1 000 to 100 000 entities.
// Numbers in BENCH.md.
package statemachine_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// benchSizes are the entity counts both benchmarks run over.
var benchSizes = []int{1_000, 10_000, 100_000}

// population fills a store and a journal with n entities in state Live, each
// having entered it a millisecond after the previous one, so exactly one
// falls due per millisecond of the clock.
func population(b *testing.B, n int) (*memStore, *memJournal) {
	b.Helper()
	store, journal := newMemStore(), newMemJournal()
	for i := range n {
		key := fmt.Sprintf("e%07d", i)
		raw, err := json.Marshal(Item{ID: key, State: Live})
		if err != nil {
			b.Fatal(err)
		}
		store.data[key] = raw
		journal.records[key] = corestm.RecordValue[State]{Key: key, State: Live, Entered: start.Add(time.Duration(i) * time.Millisecond)}
	}
	return store, journal
}

// BenchmarkNextDue measures one pass of the loop per due transition: the
// clock moves a millisecond, one entity falls due and is fired, and the one
// fired the pass before is rescheduled. Entities cycle between two states on
// a delay of n milliseconds, so the agenda always holds n entries. The cost
// must not grow with n beyond the heap's log n.
func BenchmarkNextDue(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("entities=%d", n), func(b *testing.B) {
			store, journal := population(b, n)
			period := time.Duration(n) * time.Millisecond
			clk := clock.NewManualClock(start)
			def := svcstm.NewMachineSpec(stateOf).Initial(Live).
				After("rest", period, Live, Sold).
				After("wake", period, Sold, Live)
			ctx := context.Background()
			m, err := svcstm.NewStateMachine(ctx, def, &svcstm.Config[Item, State]{Store: store, Journal: journal, Clock: clk})
			if err != nil {
				b.Fatal(err)
			}
			clk.Set(start.Add(period - time.Millisecond))
			if _, err := m.Step(ctx); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				clk.Advance(time.Millisecond)
				if _, err := m.Step(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSweepBaseline measures what a loop that re-reads the whole store on
// every wake pays to find the same one due entity — the algorithm the agenda
// replaces, and only its search: it fires nothing.
func BenchmarkSweepBaseline(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(fmt.Sprintf("entities=%d", n), func(b *testing.B) {
			store, journal := population(b, n)
			period := time.Duration(n) * time.Millisecond
			entered := make(map[string]time.Time, n)
			for key, rec := range journal.records {
				entered[key] = rec.Entered
			}
			clk := clock.NewManualClock(start.Add(period - time.Millisecond))
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				clk.Advance(time.Millisecond)
				now, due := clk.Now(), 0
				for item, err := range store.All(ctx) {
					if err != nil {
						b.Fatal(err)
					}
					if !entered[item.ID].Add(period).After(now) {
						due++
					}
				}
				if due == 0 {
					b.Fatal("the sweep found nothing due")
				}
			}
		})
	}
}
