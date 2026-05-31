package level_test

import (
	"sync"
	"testing"

	"github.com/kitsunium/sdk/internal/core/logger/level"
)

func TestNewVar(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial level.Level
		want    level.Level
	}{
		{"debug seed", level.Debug, level.Debug},
		{"info seed", level.Info, level.Info},
		{"warn seed", level.Warn, level.Warn},
		{"error seed", level.Error, level.Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := level.NewVar(tc.initial)
			//: the holder must report the level it was seeded with.
			if got := v.Level(); got != tc.want {
				t.Fatalf("NewVar(%v).Level() = %v, want %v", tc.initial, got, tc.want)
			}
		})
	}
}

func TestVar_Level(t *testing.T) {
	t.Parallel()
	//: a zero-value Var must report Info, matching the Level zero value.
	var v level.Var
	if got := v.Level(); got != level.Info {
		t.Fatalf("zero Var.Level() = %v, want %v", got, level.Info)
	}
}

func TestVar_Set(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		set  level.Level
	}{
		{"set debug", level.Debug},
		{"set warn", level.Warn},
		{"set error", level.Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := level.NewVar(level.Info)
			v.Set(tc.set)
			//: Set must be observable through the next Level read.
			if got := v.Level(); got != tc.set {
				t.Fatalf("after Set(%v), Level() = %v, want %v", tc.set, got, tc.set)
			}
		})
	}
}

func TestVar_Concurrent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		readers int
		iters   int
	}{
		{"one reader", 1, 1000},
		{"four readers", 4, 1000},
	}
	levels := []level.Level{level.Debug, level.Info, level.Warn, level.Error}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := level.NewVar(level.Info)
			var wg sync.WaitGroup
			//: a single writer goroutine continuously retunes the floor.
			wg.Go(func() {
				for i := range tc.iters {
					//: cycle through the four constants under the race detector.
					v.Set(levels[i%len(levels)])
				}
			})
			//: several reader goroutines hammer Level concurrently with the writer.
			for range tc.readers {
				wg.Go(func() {
					for range tc.iters {
						//: every observed value must be one the writer stored.
						got := v.Level()
						//: guard against a torn read landing outside the set.
						if got != level.Debug && got != level.Info && got != level.Warn && got != level.Error {
							t.Errorf("Level() = %v, not a stored value", got)
							return
						}
					}
				})
			}
			wg.Wait()
		})
	}
}

// staticLeveler is a fixed-threshold Leveler used to prove the interface admits
// a constant implementation alongside the atomic Var.
type staticLeveler level.Level

// Level returns the constant threshold.
func (s staticLeveler) Level() level.Level {
	//: a constant Leveler simply reports its embedded Level.
	return level.Level(s)
}

func TestLeveler_Interface(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		lv   level.Leveler
		want level.Level
	}{
		{"atomic Var", level.NewVar(level.Warn), level.Warn},
		{"static leveler", staticLeveler(level.Error), level.Error},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: both implementations satisfy Leveler and report their threshold.
			if got := tc.lv.Level(); got != tc.want {
				t.Fatalf("Leveler.Level() = %v, want %v", got, tc.want)
			}
		})
	}
}
