package queue

import (
	"runtime"
	"testing"
	"time"
	"weak"
)

// Test_earliest pins that zero means "none" and never wins, which is what lets
// the durable broker fold three scans that may each have found nothing.
func Test_earliest(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		instants []int64
		want     int64
	}
	tests := []tc{
		{"nothing", nil, 0},
		{"all zero", []int64{0, 0, 0}, 0},
		{"one instant among zeros", []int64{0, 7, 0}, 7},
		{"the smallest wins", []int64{9, 3, 5}, 3},
		{"zero does not beat a real instant", []int64{0, 4}, 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := earliest(c.instants...); got != c.want {
			t.Errorf("earliest(%v) = %d, want %d", c.instants, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_wakeSignal_fire pins the broadcast: a fire closes the channel every
// waiter holds and arms a new one, so the next waiter sleeps again.
func Test_wakeSignal_fire(t *testing.T) {
	t.Parallel()
	signal := newWakeSignal()
	first, _ := signal.current()
	signal.fire()
	select {
	case <-first:
	default:
		t.Fatal("fire did not close the channel handed out before it")
	}
	second, _ := signal.current()
	select {
	case <-second:
		t.Fatal("the channel armed by fire is already closed")
	default:
	}
}

// Test_directoryWakes_sharesAndForgets pins the two halves of the table: two
// lookups of one directory share a signal, and once nothing references it
// the entry is dropped, so a process that opens queues in fresh directories
// does not accumulate one entry per directory forever.
func Test_directoryWakes_sharesAndForgets(t *testing.T) {
	t.Parallel()
	table := &directoryWakes{byDir: make(map[string]weak.Pointer[wakeSignal])}
	dir := t.TempDir()
	//: the only strong references live in this closure, and die with it.
	func() {
		first := table.forDir(dir)
		if table.forDir(dir) != first {
			t.Fatal("two lookups of one directory returned two signals")
		}
		first.fire()
	}()
	deadline := time.Now().Add(10 * time.Second)
	for table.size() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the entry of a collected signal was never dropped")
		}
		runtime.GC()
		time.Sleep(time.Millisecond)
	}
}

// size returns how many directories the table holds.
func (d *directoryWakes) size() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.byDir)
}
