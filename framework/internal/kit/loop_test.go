package kit_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// shopWaits is how many waits the shop arms on its clock once the digest
// loop sleeps: the link key's rotation, the jobs' scheduler, two queues'
// consumers — each one wait at most — and the digest's timer.
const shopWaits int = 5

// Supervised is a service whose hand-written loop the tests script.
var Supervised = kit.NewService("supervised", "A hand-written loop, scripted by the tests.")

var flappy = struct {
	sync.Mutex
	script []string // what each start does: error, panic, return, wait
	starts int
}{}

var _ = Supervised.Go("flappy", Flap)

// Faraway is a service whose loop wakes on a topic of a service it does not
// always run with.
var Faraway = kit.NewService("faraway", "A loop waking on another service's topic.")

var _ = Faraway.Loop("watch", func(context.Context, kit.WakeEvent) error { return nil }, kit.WakeOn(Events))

// digestWakes returns why the digest loop ran, in order.
func digestWakes() []kit.WakeEvent {
	digests.Lock()
	defer digests.Unlock()
	return slices.Clone(digests.wakes)
}

// waitWakes waits until the digest loop has run n times.
func waitWakes(t *testing.T, n int) []kit.WakeEvent {
	t.Helper()
	eventually(t, "the digest loop to run", func() bool { return len(digestWakes()) >= n })
	w := digestWakes()
	if len(w) != n {
		t.Fatalf("the digest ran %d times, want %d: %+v", len(w), n, w)
	}
	return w
}

// digestRan waits until the digest loop's entry counts n runs. waitWakes
// sees a run once the digest starts, the entry counts it once the run ends:
// read in between, the entry says the run is going and one fewer ran.
func digestRan(t *testing.T, app *kit.App, n int64) {
	t.Helper()
	eventually(t, "the digest loop's entry to count its last run", func() bool {
		l := loopNamed(app.Graph(), "members/loop/digest")
		return l != nil && l.Runs == n
	})
}

// stillWakes checks, for a moment, that the digest loop does not run again.
func stillWakes(t *testing.T, n int) {
	t.Helper()
	time.Sleep(30 * time.Millisecond)
	if got := len(digestWakes()); got != n {
		t.Fatalf("the digest ran %d times, want still %d: %+v", got, n, digestWakes())
	}
}

func reasons(ws []kit.WakeEvent) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Reason
	}
	return out
}

func TestDeclaredLoopWakes(t *testing.T) {
	resetDigests()
	app, clk := startManual(t)
	ctx := t.Context()
	w := waitWakes(t, 1)
	if w[0].Reason != model.WakeStart || !w[0].At.Equal(clk.Now()) {
		t.Fatalf("the first run %+v", w[0])
	}

	// A publish on a WakeOn topic wakes it.
	if err := Joined.Publish(ctx, Account{ID: "acc_j"}); err != nil {
		t.Fatal(err)
	}
	w = waitWakes(t, 2)
	if w[1].Reason != model.WakeTopic || w[1].Topic != "members/topic/joined" {
		t.Fatalf("the topic wake %+v", w[1])
	}

	// A nudge wakes it, and the deadline it then answers is armed.
	digests.Lock()
	digests.due = clk.Now().Add(10 * time.Minute)
	digests.Unlock()
	Digest.Nudge()
	if w = waitWakes(t, 3); w[2].Reason != model.WakeManual {
		t.Fatalf("the nudge %+v", w[2])
	}
	armed(t, clk, shopWaits)
	clk.Advance(9 * time.Minute)
	stillWakes(t, 3)
	clk.Advance(time.Minute)
	if w = waitWakes(t, 4); w[3].Reason != model.WakeDeadline {
		t.Fatalf("the deadline wake %+v", w[3])
	}

	// Without a deadline, the interval: an hour after the last run.
	digests.Lock()
	digests.due = time.Time{}
	digests.Unlock()
	Digest.Nudge()
	waitWakes(t, 5)
	armed(t, clk, shopWaits)
	clk.Advance(59 * time.Minute)
	stillWakes(t, 5)
	clk.Advance(time.Minute)
	if w = waitWakes(t, 6); w[5].Reason != model.WakeInterval {
		t.Fatalf("the interval wake %+v", w[5])
	}

	digestRan(t, app, 6)
	g := app.Graph()
	loop := loopNamed(g, "members/loop/digest")
	if loop == nil || loop.Kind != model.LoopWake || loop.Provenance != model.ProvenanceKit || loop.Library != "kit" ||
		loop.State != model.LoopWaiting || loop.LastWake != model.WakeInterval || loop.Runs != 6 || loop.Errors != 0 ||
		loop.NextRun == nil || !loop.NextRun.Equal(clk.Now().Add(time.Hour)) {
		t.Errorf("the loop entry %+v", loop)
	}
	info := g.Node("members/loop/digest").Loop
	var kinds []string
	for _, w := range info.Wakes {
		kinds = append(kinds, w.Kind)
	}
	if info.Style != model.LoopDeclared || !slices.Equal(kinds, []string{model.WakeStart, model.WakeInterval, model.WakeDeadline, model.WakeTopic, model.WakeManual}) {
		t.Errorf("loop info %+v", info)
	}
	if e := g.Edge("members/topic/joined|wakes|members/loop/digest"); e == nil || !e.Declared {
		t.Error("the wakes edge is not declared")
	}
	if e := g.Edge("members/loop/digest|reads|members/store/accounts"); e == nil || e.Observed == nil {
		t.Error("what the loop reads is not drawn from it")
	}
	r := call(t, app, "GET /_kit/api/traces?root=members/loop/digest", noBody)
	var traces []model.Trace
	r.json(t, &traces)
	if len(traces) != 6 {
		t.Fatalf("the runs' traces: %d", len(traces))
	}
	i := slices.IndexFunc(traces[0].Spans, func(s model.Span) bool { return s.Op == model.OpRun })
	if i < 0 || traces[0].Spans[i].Node != "members/loop/digest" || traces[0].Spans[i].Attrs["wake"] != model.WakeInterval || traces[0].Spans[i].ParentID != "" {
		t.Errorf("the newest run's spans: %+v", traces[0].Spans)
	}
}

// Wakes that come during a run are one run after it.
func TestLoopWakesAreCoalesced(t *testing.T) {
	resetDigests()
	gate := make(chan struct{})
	digests.Lock()
	digests.gate = gate
	digests.Unlock()
	startManual(t)
	waitWakes(t, 1) // the start run, held at the gate
	for range 5 {
		if err := Joined.Publish(t.Context(), Account{ID: "acc_burst"}); err != nil {
			t.Fatal(err)
		}
	}
	Digest.Nudge()
	digests.Lock()
	digests.gate = nil
	digests.Unlock()
	close(gate)
	w := waitWakes(t, 2)
	stillWakes(t, 2)
	if w[1].Reason != model.WakeTopic {
		t.Errorf("the burst ran for %+v, want the first reason, a topic", w[1])
	}
}

// A failed run makes the next one wait — a backoff doubling from a second —
// except a nudge; a panic is a failure the loop survives.
func TestLoopFailuresBackOff(t *testing.T) {
	resetDigests()
	gate := make(chan struct{})
	digests.Lock()
	digests.fail = 2
	digests.gate = gate
	digests.Unlock()
	app, clk := startManual(t)
	ctx := t.Context()
	waitWakes(t, 1) // held at the gate; it fails: the next run waits a second

	// A publish during that run is a wake already there when the backoff
	// starts: the loop takes it before it arms a timer and holds it until
	// the second is over — its one timer, armed before the clock moves. Came
	// once the loop slept, it would make the loop drop its timer and arm
	// another, which no count of the armed waits can tell apart.
	if err := Joined.Publish(ctx, Account{ID: "acc_1"}); err != nil {
		t.Fatal(err)
	}
	digests.Lock()
	digests.gate = nil
	digests.Unlock()
	close(gate)
	armed(t, clk, shopWaits)
	clk.Advance(900 * time.Millisecond)
	stillWakes(t, 1)
	clk.Advance(100 * time.Millisecond)
	if w := waitWakes(t, 2); w[1].Reason != model.WakeTopic {
		t.Fatalf("the held wake %+v", w[1])
	}
	// Failed again: two seconds now — but a nudge does not wait.
	Digest.Nudge()
	waitWakes(t, 3)

	digests.Lock()
	digests.panics = 1
	digests.Unlock()
	Digest.Nudge()
	waitWakes(t, 4)
	Digest.Nudge()
	waitWakes(t, 5)
	digestRan(t, app, 5)
	loop := loopNamed(app.Graph(), "members/loop/digest")
	if loop == nil || loop.Runs != 5 || loop.Errors != 3 || loop.LastError != "" && strings.Contains(loop.LastError, "do-not-leak") {
		t.Errorf("the loop entry %+v", loop)
	}
	r := call(t, app, "GET /_kit/api/traces?root=members/loop/digest", noBody)
	if strings.Contains(string(r.body), "do-not-leak") {
		t.Error("the panic reached a span")
	}
}

// A deadline already past runs the loop, but not in a spin.
func TestPastDeadlinesDoNotSpin(t *testing.T) {
	resetDigests()
	digests.Lock()
	digests.due = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	digests.Unlock()
	_, clk := startManual(t)
	waitWakes(t, 1)
	stillWakes(t, 1)
	armed(t, clk, shopWaits)
	clk.Advance(900 * time.Millisecond)
	stillWakes(t, 1)
	clk.Advance(100 * time.Millisecond)
	if w := waitWakes(t, 2); w[1].Reason != model.WakeDeadline {
		t.Fatalf("the past deadline %+v", w[1])
	}
}

// Flap does what the script says for this start.
func Flap(ctx context.Context) error {
	flappy.Lock()
	action := "wait"
	if flappy.starts < len(flappy.script) {
		action = flappy.script[flappy.starts]
	}
	flappy.starts++
	flappy.Unlock()
	switch action {
	case "error":
		return errors.New("flap")
	case "panic":
		panic("canary: do-not-leak-routine-4f5a")
	case "return":
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func flapStarts() int {
	flappy.Lock()
	defer flappy.Unlock()
	return flappy.starts
}

func TestHandWrittenLoopIsSupervised(t *testing.T) {
	flappy.Lock()
	flappy.script, flappy.starts = []string{"error", "panic", "return"}, 0
	flappy.Unlock()
	clk := clock.NewManualClock(time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC))
	app := kit.NewApp("supervised", Supervised).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard), kit.Clock(clk))
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			app.Stop(context.Background())
		}
	}()
	restartAfter := func(n int, wait time.Duration) {
		t.Helper()
		eventually(t, "the loop to wait for its restart", func() bool {
			l := loopNamed(app.Graph(), "supervised/loop/flappy")
			return l != nil && l.State == model.LoopRestarting && flapStarts() == n
		})
		// The supervisor says it restarts, then arms its backoff — the one
		// wait on the clock.
		armed(t, clk, 1)
		clk.Advance(wait - 10*time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if got := flapStarts(); got != n {
			t.Fatalf("restarted before its backoff of %s: %d starts", wait, got)
		}
		clk.Advance(10 * time.Millisecond)
		eventually(t, "the restart", func() bool { return flapStarts() == n+1 })
	}
	restartAfter(1, time.Second)   // after the error
	restartAfter(2, 2*time.Second) // after the panic
	restartAfter(3, 4*time.Second) // after an early return
	eventually(t, "the loop to run", func() bool {
		l := loopNamed(app.Graph(), "supervised/loop/flappy")
		return l.State == model.LoopRunning
	})
	l := loopNamed(app.Graph(), "supervised/loop/flappy")
	if l.Kind != model.LoopRoutine || l.Provenance != model.ProvenanceProduct || l.Restarts != 3 || l.Runs != 3 || l.Errors != 2 {
		t.Errorf("the loop entry %+v", l)
	}
	if strings.Contains(l.LastError, "do-not-leak") {
		t.Errorf("the panic reached the loop entry: %q", l.LastError)
	}
	if n := app.Graph().Node("supervised/loop/flappy"); n.Loop == nil || n.Loop.Style != model.LoopGoroutine {
		t.Errorf("node %+v", n)
	}
	// Stop cancels the loop's context and waits for it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	stopped = true
	final := loopNamed(app.Graph(), "supervised/loop/flappy")
	if final != nil && final.State != model.LoopStopped {
		t.Errorf("after Stop the loop is %s", final.State)
	}
}

func TestLoopDeclarationProblems(t *testing.T) {
	svc := kit.NewService("bad-loops", "")
	svc.Loop("never", func(context.Context, kit.WakeEvent) error { return nil }, kit.WakeEvery(0))
	svc.Loop("nowhere", func(context.Context, kit.WakeEvent) error { return nil }, kit.WakeOn[Event](nil))
	svc.Loop("nothing", nil)
	svc.Go("nobody", nil)
	svc.Go("never", func(context.Context) error { return nil })
	err := kit.NewApp("x", svc).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	for _, want := range []string{
		`loop "never": WakeEvery needs a positive period`,
		`loop "nowhere": WakeOn has a nil topic`,
		`loop "nothing" has a nil function`,
		`routine "nobody" has a nil function`,
		`service "bad-loops" already declares a loop named "never"`,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Start lacks %q: %v", want, err)
		}
	}
	err = kit.NewApp("x", Faraway).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Logs(io.Discard)).Start(t.Context())
	if !errs.HasCode(err, kit.CodeLoopNotMounted) {
		t.Errorf("a loop waking on an unmounted topic started: %v", err)
	}
}
