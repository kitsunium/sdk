// Package profiling — white-box tests of the counted goroutine profile, of the
// label matching that relies on it and its fallback, and of the frame budget.
package profiling

import (
	"errors"
	"slices"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
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

// TestGoroutinesStandWithoutTheLabelledProfile pins the fallback: when the
// labelled profile cannot be written, the dump's goroutines are returned
// without labels rather than dropped.
func TestGoroutinesStandWithoutTheLabelledProfile(t *testing.T) {
	t.Parallel()
	dump := "goroutine 1 [running]:\nmain.main()\n\t/src/main.go:30 +0x13f\n"
	gs, err := goroutinesFrom(func(debug int) ([]byte, error) {
		if debug == debugCounts {
			return nil, errors.New("the labelled profile could not be written")
		}
		return []byte(dump), nil
	})
	if err != nil || len(gs) != 1 || gs[0].ID != 1 || gs[0].Labels != nil {
		t.Fatalf("goroutinesFrom() = %+v, %v; want the one goroutine, unlabelled", gs, err)
	}
	if _, err := goroutinesFrom(func(int) ([]byte, error) { return nil, errors.New("no dump") }); err == nil {
		t.Error("a dump that cannot be written must fail the call")
	}
}

// deep is a decoder whose one location has lines inlined lines, named by one
// sample refs times.
func deep(lines, refs int) *decoder {
	loc := rawLocation{lines: make([]rawLine, lines)}
	for i := range loc.lines {
		loc.lines[i] = rawLine{function: 1, line: int64(i + 1)}
	}
	return &decoder{
		strings:   []string{"", "samples", "count", "app.f", "/src/app.go"},
		types:     []rawType{{typ: 1, unit: 2}},
		functions: map[uint64]rawFunction{1: {name: 3, file: 4}},
		locations: map[uint64]rawLocation{1: loc},
		samples:   []rawSample{{locations: slices.Repeat([]uint64{1}, refs), values: []int64{1}}},
	}
}

// TestTheFramesAreBoundedBeforeTheyAreBuilt pins MaxFrames: a location's
// frames and every stack's copy of them count, and the budget is checked
// before the frames exist — a location three deep named four times is 3 + 12.
func TestTheFramesAreBoundedBeforeTheyAreBuilt(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		d      *decoder
		budget int
		fits   bool
	}
	cases := []tc{
		{"exactly the budget", deep(3, 4), 15, true},
		{"one frame over, in a stack", deep(3, 4), 14, false},
		{"a location deeper than the budget", deep(20, 1), 10, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		p, err := c.d.resolve(c.budget)
		if c.fits {
			if err != nil || len(p.Samples[0].Stack) != 12 {
				t.Fatalf("resolve() = %v; want a stack of 12", err)
			}
			return
		}
		if p != nil || !errs.HasCode(err, CodeProfileTooLarge) {
			t.Fatalf("resolve() = %v, %v; want PROFILE_TOO_LARGE", p, err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
