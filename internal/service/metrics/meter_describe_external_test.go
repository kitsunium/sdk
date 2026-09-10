// Package metrics_test — the in-memory meter's Describer half (ADR 0067):
// what a description does to a snapshot, and the two calls it refuses.
package metrics_test

import (
	"strings"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// asDescriber reaches the sibling port through the union NewMeter returns. The
// assertion always holds for this SDK's meter; it is written out because the
// FALSE branch is the documented answer for a meter that records no
// description, and a helper that hid it would hide the port's whole shape.
func asDescriber(t *testing.T, meter coremetrics.FullMeter) coremetrics.Describer {
	t.Helper()
	describer, ok := meter.(coremetrics.Describer)
	//: a Meter that is not one would be a foreign implementation.
	if !ok {
		t.Fatal("the in-memory meter does not implement Describer")
	}
	return describer
}

// TestDescribeReachesEveryKindOfSnapshotEnvelope pins the whole point of the
// feature: a description written once, at wiring time, appears on the metric
// envelope of every instrument kind and on none of the data points.
func TestDescribeReachesEveryKindOfSnapshotEnvelope(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	describer := asDescriber(t, meter)

	describer.Describe("requests_total", "Requests served")
	describer.Describe("in_flight", "Requests currently being served")
	describer.Describe("queue_depth", "Items waiting in the queue")
	describer.Describe("latency_seconds", "Handler latency")

	meter.Counter("requests_total").Inc()
	meter.UpDownCounter("in_flight").Inc()
	meter.Gauge("queue_depth").Set(3)
	meter.Histogram("latency_seconds", []float64{1}).Record(0.5)

	snap := meter.Collect()
	//: a sum envelope carries it whether the sum is monotonic or not.
	if got := snap.Sums["requests_total"].Description; got != "Requests served" {
		t.Errorf("counter description = %q, want %q", got, "Requests served")
	}
	if got := snap.Sums["in_flight"].Description; got != "Requests currently being served" {
		t.Errorf("up-down counter description = %q", got)
	}
	if got := snap.Gauges["queue_depth"].Description; got != "Items waiting in the queue" {
		t.Errorf("gauge description = %q", got)
	}
	if got := snap.Histograms["latency_seconds"].Description; got != "Handler latency" {
		t.Errorf("histogram description = %q", got)
	}
}

// TestDescribeReachesAnObservableInstrument pins the third registration path.
//
// An observable binds its name through register/bindName rather than through a
// series fetch, so it is a different route to the same map — and its series
// only exist after the callback has run inside Collect, which is exactly where
// a description looked up too early would come back empty.
func TestDescribeReachesAnObservableInstrument(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	asDescriber(t, meter).Describe("goroutines", "Live goroutines")
	meter.ObservableUpDownCounter("goroutines", func(observe coremetrics.ObserveInt64) {
		observe(7)
	})

	snap := meter.Collect()
	if got := snap.Sums["goroutines"].Description; got != "Live goroutines" {
		t.Errorf("observable description = %q, want %q", got, "Live goroutines")
	}
	if got := snap.Sums["goroutines"].Points[0].Value; got != 7 {
		t.Errorf("observable value = %d, want 7", got)
	}
}

// TestDescribeIsIndependentOfInstrumentOrder pins that a description binds to a
// NAME and not to an instrument, in both directions.
//
// Describing first is the natural wiring order — the description sits next to
// the decision that the metric exists — and requiring the instrument to be
// minted first would be an ordering rule with no reason behind it. Describing
// afterwards has to work too, because two packages may split the two calls.
func TestDescribeIsIndependentOfInstrumentOrder(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	describer := asDescriber(t, meter)

	//: described BEFORE the instrument exists.
	describer.Describe("early_total", "Described first")
	meter.Counter("early_total").Inc()
	//: described AFTER the instrument exists.
	meter.Counter("late_total").Inc()
	describer.Describe("late_total", "Described second")
	//: described and never instrumented at all.
	describer.Describe("ghost_total", "No instrument ever minted this")

	snap := meter.Collect()
	if got := snap.Sums["early_total"].Description; got != "Described first" {
		t.Errorf("describe-then-mint = %q, want %q", got, "Described first")
	}
	if got := snap.Sums["late_total"].Description; got != "Described second" {
		t.Errorf("mint-then-describe = %q, want %q", got, "Described second")
	}
	//: a description with no metric has nowhere to be wrong — it is simply
	//: absent from the snapshot rather than a metric with no points.
	if _, present := snap.Sums["ghost_total"]; present {
		t.Error("a described-but-never-minted name produced a metric")
	}
}

// TestUndescribedMetricsCarryTheEmptyString pins the absent case at the source.
// An undescribed meter never allocates the map at all, so this also exercises
// the nil-map read every Collect performs when nobody describes anything.
func TestUndescribedMetricsCarryTheEmptyString(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	meter.Counter("requests_total").Inc()
	meter.Gauge("queue_depth").Set(1)
	meter.Histogram("latency_seconds", []float64{1}).Record(0.5)

	snap := meter.Collect()
	//: "" is the honest answer for "nobody wrote one", and every exporter
	//: turns it into an omission rather than a blank.
	if got := snap.Sums["requests_total"].Description; got != "" {
		t.Errorf("an undescribed counter reads %q, want the empty string", got)
	}
	if got := snap.Gauges["queue_depth"].Description; got != "" {
		t.Errorf("an undescribed gauge reads %q, want the empty string", got)
	}
	if got := snap.Histograms["latency_seconds"].Description; got != "" {
		t.Errorf("an undescribed histogram reads %q, want the empty string", got)
	}
}

// TestDescribeIsIdempotentForIdenticalText pins the case that must NOT be a
// conflict: two packages documenting one metric the same way have not
// disagreed about anything, and refusing them would make a shared instrument
// impossible to describe from more than one place.
func TestDescribeIsIdempotentForIdenticalText(t *testing.T) {
	t.Parallel()
	meter := svcmetrics.NewMeter()
	describer := asDescriber(t, meter)
	meter.Counter("requests_total").Inc()

	describer.Describe("requests_total", "Requests served")
	describer.Describe("requests_total", "Requests served")
	describer.Describe("requests_total", "Requests served")

	if got := meter.Collect().Sums["requests_total"].Description; got != "Requests served" {
		t.Errorf("description = %q after three identical Describes", got)
	}
}

// TestDescribeRefusesTheTwoProgrammerErrors pins both panics.
//
// Neither is a runtime condition. A description is a literal at a wiring site,
// constant for the process, so both of these fail on the first boot or never —
// which is the same argument bindName makes for InstrumentKindConflict, and the
// reason a panic is proportionate rather than brutal.
func TestDescribeRefusesTheTwoProgrammerErrors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: describe is what runs after the meter has been set up.
		describe func(coremetrics.Describer)
		//: want is a substring of the sentinel the panic must carry.
		want string
	}
	tests := []tc{
		{
			name:     "an empty description would document nothing",
			describe: func(d coremetrics.Describer) { d.Describe("requests_total", "") },
			want:     "INVALID_DESCRIPTION",
		},
		{
			name: "a second, different description for one name",
			describe: func(d coremetrics.Describer) {
				d.Describe("requests_total", "Requests served")
				d.Describe("requests_total", "Something else entirely")
			},
			want: "DESCRIPTION_CONFLICT",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		describer := asDescriber(t, svcmetrics.NewMeter())
		defer func() {
			recovered := recover()
			//: the refusal must be loud.
			if recovered == nil {
				t.Fatalf("%s did not panic", c.name)
			}
			//: and it must name which of the two mistakes was made.
			message, ok := recovered.(string)
			if !ok || !strings.Contains(message, c.want) {
				t.Fatalf("panic = %v, want one carrying %q", recovered, c.want)
			}
		}()
		c.describe(describer)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDescribeIsSafeAlongsideCollectAndObservations is the race-detector case.
//
// Describe takes the meter's write lock and Collect reads the same map under
// the read lock it already holds; the description map is the only state this
// feature added, so it is the only thing that could be torn.
//
// Goroutine lifecycle: `workers` describers, each of which returns after one
// Describe, one observation and one Collect. The WaitGroup is what joins them,
// and it is waited on before any assertion, so nothing is left running.
func TestDescribeIsSafeAlongsideCollectAndObservations(t *testing.T) {
	t.Parallel()
	const workers int = 8
	meter := svcmetrics.NewMeter()
	describer := asDescriber(t, meter)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			//: every worker writes the SAME text, which is the idempotent
			//: path — a conflicting one would panic and is not a race.
			describer.Describe("requests_total", "Requests served")
			meter.Counter("requests_total").Inc()
			meter.Collect()
		}()
	}
	wg.Wait()

	snap := meter.Collect()
	if got := snap.Sums["requests_total"].Description; got != "Requests served" {
		t.Errorf("description = %q after %d concurrent describers", got, workers)
	}
	if got := snap.Sums["requests_total"].Points[0].Value; got != int64(workers) {
		t.Errorf("counter = %d, want %d", got, workers)
	}
}
