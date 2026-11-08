package memlimit

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// watchPeriod is the period the Watch tests run at: the floor, so the clamp
// leaves it alone.
const watchPeriod time.Duration = time.Second

// mutableFS is a cgroup filesystem a test rewrites between two ticks, and that
// signals each derivation on reads.
type mutableFS struct {
	mu    sync.Mutex
	files map[string]string
	reads chan struct{}
}

// read serves a file, and signals a derivation when it is the membership
// file every derivation reads first.
func (f *mutableFS) read(name string) ([]byte, error) {
	f.mu.Lock()
	body, ok := f.files[name]
	f.mu.Unlock()
	//: one derivation started.
	if name == procSelfCgroup {
		f.reads <- struct{}{}
	}
	//: an absent file, as the OS says it.
	if !ok {
		return nil, errNoFile
	}
	return []byte(body), nil
}

// set rewrites one file.
func (f *mutableFS) set(name, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = body
}

// limitRecorder counts and keeps every limit and GC percent installed.
type limitRecorder struct {
	mu     sync.Mutex
	limits []int64
	gc     []int
}

func (r *limitRecorder) setLimit(limit int64) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limits = append(r.limits, limit)
	return 0
}

func (r *limitRecorder) setGC(percent int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gc = append(r.gc, percent)
	return 100
}

func (r *limitRecorder) snapshot() (limits []int64, gc []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.limits...), append([]int(nil), r.gc...)
}

// TestWatch_ReDerivesOncePerChange drives Watch on a ManualClock: the limit
// is installed once at start, never again across 1 000 unchanged ticks, once
// per allowance change, and the runtime default comes back when the cap is
// lifted.
func TestWatch_ReDerivesOncePerChange(t *testing.T) {
	t.Parallel()
	const oneGiB int64 = 1 << 30
	fs := &mutableFS{
		files: map[string]string{procSelfCgroup: "0::/app\n", "/sys/fs/cgroup/app/memory.max": "1073741824"},
		reads: make(chan struct{}, 1),
	}
	rec := &limitRecorder{}
	clk := clock.NewManualClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(t.Context())
	changes := make(chan coreproc.MemoryLimitValue, 8)
	done := make(chan error, 1)
	env := environment{lookup: noEnv, readFile: fs.read, setLimit: rec.setLimit, setGC: rec.setGC}
	go func() {
		done <- watchFrom(ctx, clk, watchPeriod, ConfigValue{}, env, func(v coreproc.MemoryLimitValue) { changes <- v })
	}()
	<-fs.reads
	first := <-changes
	//: the start applies and reports, as ApplyWith would.
	if first.Limit != deriveLimit(oneGiB) || first.Bound != coreproc.MemoryBoundMax {
		t.Fatalf("first = %+v, want the 90 %% share of 1 GiB", first)
	}
	tick := func() {
		clk.BlockUntil(1)
		clk.Advance(watchPeriod)
		<-fs.reads
	}
	//: 1 000 ticks over unchanged files install nothing more.
	for range 1000 {
		tick()
	}
	limits, _ := rec.snapshot()
	if len(limits) != 1 || len(changes) != 0 {
		t.Fatalf("after 1 000 unchanged ticks: limits %v, %d change(s); want the one start install", limits, len(changes))
	}
	//: a resize between two ticks is installed once and reported once.
	fs.set("/sys/fs/cgroup/app/memory.max", "2147483648")
	tick()
	resized := <-changes
	tick()
	limits, _ = rec.snapshot()
	if resized.Allowance != 2*oneGiB || len(limits) != 2 || limits[1] != deriveLimit(2*oneGiB) {
		t.Fatalf("after a resize: change %+v, limits %v; want one install of the new share", resized, limits)
	}
	//: a lifted cap restores the runtime's default rather than keeping a
	//: limit no cap stands behind.
	fs.set("/sys/fs/cgroup/app/memory.max", "max")
	tick()
	lifted := <-changes
	limits, _ = rec.snapshot()
	if lifted.Source != coreproc.MemorySourceUnconstrained || len(limits) != 3 || limits[2] != math.MaxInt64 {
		t.Fatalf("after a lift: change %+v, limits %v; want the default restored", lifted, limits)
	}
	cancel()
	//: a cancelled watch ends quietly.
	if err := <-done; err != nil {
		t.Fatalf("Watch ended with %v, want nil", err)
	}
}

// TestWatch_ClampsThePeriod pins ADR 0031's clamp: a period under a second
// waits a second.
func TestWatch_ClampsThePeriod(t *testing.T) {
	t.Parallel()
	fs := &mutableFS{files: map[string]string{procSelfCgroup: "0::/\n"}, reads: make(chan struct{}, 1)}
	rec := &limitRecorder{}
	clk := clock.NewManualClock(time.Unix(0, 0))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	env := environment{lookup: noEnv, readFile: fs.read, setLimit: rec.setLimit, setGC: rec.setGC}
	go func() { _ = watchFrom(ctx, clk, 0, ConfigValue{}, env, func(coreproc.MemoryLimitValue) {}) }()
	<-fs.reads
	clk.BlockUntil(1)
	clk.Advance(watchPeriod - time.Millisecond)
	//: not yet due: no derivation.
	select {
	case <-fs.reads:
		t.Fatal("a zero period re-read before a second")
	case <-time.After(20 * time.Millisecond):
	}
	clk.Advance(time.Millisecond)
	<-fs.reads
}

// TestApplyWith_Config pins the knobs: the zero value is Apply, the headroom
// and the floor are the caller's, GCPercent is set with the limit and only
// then, and a value out of range is refused.
func TestApplyWith_Config(t *testing.T) {
	t.Parallel()
	const oneGiB int64 = 1 << 30
	files := map[string]string{procSelfCgroup: "0::/\n", "/sys/fs/cgroup/memory.max": "1073741824"}
	off := -1
	type tc struct {
		name      string
		cfg       ConfigValue
		wantLimit int64
		wantGC    []int
		wantCode  errs.Code
	}
	tests := []tc{
		{name: "zero is Apply", cfg: ConfigValue{}, wantLimit: deriveLimit(oneGiB)},
		{name: "headroom", cfg: ConfigValue{HeadroomPercent: 50}, wantLimit: oneGiB / 2},
		{name: "full share", cfg: ConfigValue{HeadroomPercent: 100}, wantLimit: oneGiB},
		{name: "floor above the share declines", cfg: ConfigValue{MinimumLimit: 2 * oneGiB}},
		{name: "GC off with the limit", cfg: ConfigValue{GCPercent: &off}, wantLimit: deriveLimit(oneGiB), wantGC: []int{-1}},
		{name: "headroom above 100", cfg: ConfigValue{HeadroomPercent: 150}, wantCode: coreproc.CodeMemlimitMisconfigured},
		{name: "negative headroom", cfg: ConfigValue{HeadroomPercent: -1}, wantCode: coreproc.CodeMemlimitMisconfigured},
		{name: "negative floor", cfg: ConfigValue{MinimumLimit: -1}, wantCode: coreproc.CodeMemlimitMisconfigured},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rec := &limitRecorder{}
		env := environment{lookup: noEnv, readFile: fakeFS(files), setLimit: rec.setLimit, setGC: rec.setGC}
		got, err := applyWithFrom(c.cfg, env)
		limits, gc := rec.snapshot()
		//: a refusal installs nothing.
		if c.wantCode != 0 {
			if !errs.HasCode(err, c.wantCode) || len(limits) != 0 || len(gc) != 0 {
				t.Fatalf("ApplyWith(%+v) = %v, %v, limits %v; want %v and nothing installed", c.cfg, got, err, limits, c.wantCode)
			}
			return
		}
		if err != nil || got.Limit != c.wantLimit || len(gc) != len(c.wantGC) {
			t.Fatalf("ApplyWith(%+v) = %+v, %v, gc %v; want limit %d, gc %v", c.cfg, got, err, gc, c.wantLimit, c.wantGC)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) { t.Parallel(); runCase(t, c) })
	}
	//: the zero config through the public path equals Apply's.
	if a, b := applyFrom(noEnv, fakeFS(files), func(int64) int64 { return 0 }), deriveWith(ConfigValue{}.resolved(), noEnv, fakeFS(files)); a != b {
		t.Fatalf("Apply = %+v, ApplyWith(zero) derives %+v", a, b)
	}
}
