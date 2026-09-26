// Package profiling — white-box tests of the counted goroutine profile and of
// the label matching that relies on it.
package profiling

import (
	"slices"
	"testing"
)

// counted is a debug=1 goroutine profile as linux/arm64 writes it: tabwriter
// aligns the columns, so the frames after a shorter address carry empty ones.
const counted = "goroutine profile: total 4\n" +
	"3 @ 0xc7938 0xa20b4 0x2e5324 0xd7698 0xceb44\n" +
	"# labels: {\"node\":\"waiter\", \"odd key\":\"va\\\"l, x\"}\n" +
	"#\t0x2e5323\texample.com/app.park+0xd3\t\t/src/app/park.go:137\n" +
	"#\t0xd7697\t\tsync.(*WaitGroup).Go.func1+0x47\t\t\t/usr/local/go/src/sync/waitgroup.go:258\n" +
	"\n" +
	"1 @ 0x4cdbc 0x88af4\n" +
	"#\t0xe007f\tmain.main+0x13f\t/src/main.go:30\n"

// TestTheCountedProfileReadsPastItsAlignmentTabs pins the frames of a record
// whose columns carry empty ones, and its labels in Go's %q quoting.
func TestTheCountedProfileReadsPastItsAlignmentTabs(t *testing.T) {
	t.Parallel()
	records := parseCounts([]byte(counted))
	if len(records) != 2 {
		t.Fatalf("%d records", len(records))
	}
	first := records[0]
	if first.count != 3 || first.labels["node"] != "waiter" || first.labels["odd key"] != `va"l, x` {
		t.Errorf("first record = %+v", first)
	}
	if want := []string{"example.com/app.park", "sync.(*WaitGroup).Go.func1"}; !slices.Equal(first.stack, want) {
		t.Errorf("first record's stack = %q; want %q", first.stack, want)
	}
}

// TestLabelsAreMatchedOneGoroutinePerCount pins the matching: three counted
// goroutines give their labels to three goroutines of the dump with the same
// stack, a fourth gets none, and each gets its own copy of the map.
func TestLabelsAreMatchedOneGoroutinePerCount(t *testing.T) {
	t.Parallel()
	frames := []FrameValue{{Function: "example.com/app.park"}, {Function: "sync.(*WaitGroup).Go.func1"}, {Function: "runtime.goexit"}}
	gs := make([]GoroutineValue, 4)
	for i := range gs {
		gs[i].Stack = frames
	}
	joinLabels(gs, parseCounts([]byte(counted)))
	for i, g := range gs[:3] {
		if g.Labels["node"] != "waiter" {
			t.Errorf("goroutine %d = %v", i, g.Labels)
		}
	}
	if gs[3].Labels != nil {
		t.Errorf("a fourth goroutine took labels the profile counted three times: %v", gs[3].Labels)
	}
	gs[0].Labels["node"] = "changed"
	if gs[1].Labels["node"] != "waiter" {
		t.Error("two goroutines share one labels map")
	}
}
