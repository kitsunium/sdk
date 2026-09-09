package metrics_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/metrics"
)

// TestFacadeAttrs pins the attributed surface through the public names only —
// the alias set is what a consumer actually compiles against, and a missing
// re-export is invisible to every test written inside the SDK.
//
// The typed cases are the point: an attribute's KIND is part of the series
// identity, so String("v", "1") and Int64("v", 1) are two series and not one.
func TestFacadeAttrs(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the two attribute sets recorded, then compared.
		first, second []metrics.Attr
		wantSeries    int
	}
	tests := []tc{
		{name: "no attributes", wantSeries: 1},
		{
			name:       "the same set twice",
			first:      []metrics.Attr{metrics.String("method", "GET")},
			second:     []metrics.Attr{metrics.String("method", "GET")},
			wantSeries: 1,
		},
		{
			//: order is not identity — an attribute set is a set.
			name:       "the same set, reordered",
			first:      []metrics.Attr{metrics.String("a", "1"), metrics.Int64("b", 2)},
			second:     []metrics.Attr{metrics.Int64("b", 2), metrics.String("a", "1")},
			wantSeries: 1,
		},
		{
			name:       "two values of one dimension",
			first:      []metrics.Attr{metrics.String("method", "GET")},
			second:     []metrics.Attr{metrics.String("method", "POST")},
			wantSeries: 2,
		},
		{
			//: the kind is part of the identity, which is the whole reason the
			//: model has typed attributes rather than strings.
			name:       "one spelling, two kinds",
			first:      []metrics.Attr{metrics.String("v", "1")},
			second:     []metrics.Attr{metrics.Int64("v", 1)},
			wantSeries: 2,
		},
		{
			name:       "a bool and its spelling",
			first:      []metrics.Attr{metrics.Bool("v", true)},
			second:     []metrics.Attr{metrics.String("v", "true")},
			wantSeries: 2,
		},
		{
			name:       "two doubles",
			first:      []metrics.Attr{metrics.Float64("v", 0.5)},
			second:     []metrics.Attr{metrics.Float64("v", 1.5)},
			wantSeries: 2,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeter()
		m.Counter("requests", c.first...).Inc()
		m.Counter("requests", c.second...).Inc()

		metric := m.Collect().Sums["requests"]
		if len(metric.Points) != c.wantSeries {
			t.Fatalf("%d series, want %d", len(metric.Points), c.wantSeries)
		}
		//: a Counter is a MONOTONIC sum, and the snapshot says so — that is
		//: the field a backend reads to decide what a decrease means.
		if !metric.Monotonic {
			t.Error("a Counter produced a non-monotonic sum")
		}
		//: nothing is lost whichever way the sets resolved.
		var total int64
		for _, p := range metric.Points {
			total += p.Value
		}
		if total != 2 {
			t.Errorf("the series total %d, want 2", total)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeAttrAccessors pins the read side of the typed model. An exporter
// outside this SDK — an OTLP encoder, most of all — reaches an attribute's
// value only through Kind plus the four accessors, so a wrong kind returning a
// plausible-looking zero would be an encoder emitting silent nonsense.
func TestFacadeAttrAccessors(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		attr     metrics.Attr
		wantKind metrics.AttrKind
		wantStr  string
		wantBool bool
		wantInt  int64
		wantF64  float64
	}
	tests := []tc{
		{
			name: "a string", attr: metrics.String("k", "v"),
			wantKind: metrics.AttrKindString, wantStr: "v",
		},
		{
			name: "a true bool", attr: metrics.Bool("k", true),
			wantKind: metrics.AttrKindBool, wantBool: true,
		},
		{
			name: "a false bool", attr: metrics.Bool("k", false),
			wantKind: metrics.AttrKindBool,
		},
		{
			name: "a negative integer", attr: metrics.Int64("k", -7),
			wantKind: metrics.AttrKindInt64, wantInt: -7,
		},
		{
			name: "a double", attr: metrics.Float64("k", 1.5),
			wantKind: metrics.AttrKindFloat64, wantF64: 1.5,
		},
		{
			//: a struct literal never sets a value, and the kind says so
			//: rather than pretending the value is the empty string.
			name: "a value no constructor set", attr: metrics.Attr{Key: "k"},
			wantKind: metrics.AttrKindInvalid,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.attr.Key; got != "k" {
			t.Errorf("Key = %q, want k", got)
		}
		if got := c.attr.Kind(); got != c.wantKind {
			t.Errorf("Kind() = %d, want %d", got, c.wantKind)
		}
		//: every accessor but the matching one reports its zero, so a reader
		//: that forgot to switch on Kind gets a zero rather than another
		//: kind's bits reinterpreted.
		if got := c.attr.Str(); got != c.wantStr {
			t.Errorf("Str() = %q, want %q", got, c.wantStr)
		}
		if got := c.attr.Bool(); got != c.wantBool {
			t.Errorf("Bool() = %v, want %v", got, c.wantBool)
		}
		if got := c.attr.Int64(); got != c.wantInt {
			t.Errorf("Int64() = %d, want %d", got, c.wantInt)
		}
		if got := c.attr.Float64(); got != c.wantF64 {
			t.Errorf("Float64() = %v, want %v", got, c.wantF64)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeUpDownCounter pins the instrument the frozen Meter port cannot
// mint, reached through the sibling interface rather than through a widened
// Meter (ADR 0039).
//
// It also pins the field that distinguishes it in the snapshot: a Counter and
// an UpDownCounter produce the SAME point shape and differ only by
// SumMetric.Monotonic — which is the OTel data model's own economy.
func TestFacadeUpDownCounter(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()
	m.UpDownCounter("in_flight").Add(5)
	m.UpDownCounter("in_flight").Add(-2)
	m.UpDownCounter("in_flight").Dec()
	m.Counter("requests").Inc()

	sums := m.Collect().Sums
	updown, ok := sums["in_flight"]
	if !ok {
		t.Fatal("the up-down counter is absent from the snapshot")
	}
	if updown.Monotonic {
		t.Error("an UpDownCounter produced Monotonic = true")
	}
	if got := updown.Points[0].Value; got != 2 {
		t.Errorf("the up-down counter reads %d, want 2", got)
	}
	//: both kinds live in ONE map, told apart by Monotonic alone.
	if !sums["requests"].Monotonic {
		t.Error("a Counter produced Monotonic = false")
	}

	//: and a narrow Meter parameter still accepts the same value — widening a
	//: returned VALUE from Meter to FullMeter is what ADR 0039 calls safe.
	narrow := metrics.Meter(m)
	if narrow.Collect().Sums == nil {
		t.Error("the FullMeter does not satisfy the frozen Meter port")
	}
}

// TestFacadeTemporality pins the concept the OTel model exists to make
// explicit, through the public surface.
//
// Cumulative REPEATS: two collections of an untouched meter report the same
// total twice, and the window's start never moves. Delta CONSUMES: the second
// collection reports zero, and the start advances to the previous end. A
// consumer that mixed the two up would double-count every observation for as
// long as the process lived.
func TestFacadeTemporality(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		temporality metrics.Temporality
		want        metrics.Temporality
		wantSecond  int64
		startMoves  bool
	}
	tests := []tc{
		{
			//: unset is not undecided: it resolves to what the meter DOES.
			name: "the zero value resolves to cumulative",
			want: metrics.TemporalityCumulative, wantSecond: 3,
		},
		{
			name: "cumulative repeats the total", temporality: metrics.TemporalityCumulative,
			want: metrics.TemporalityCumulative, wantSecond: 3,
		},
		{
			name: "delta consumes the window", temporality: metrics.TemporalityDelta,
			want: metrics.TemporalityDelta, wantSecond: 0, startMoves: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeterWithConfig(metrics.MeterConfig{Temporality: c.temporality})
		m.Counter("requests").Add(3)

		first := m.Collect()
		if got := first.Sums["requests"].Temporality; got != c.want {
			t.Errorf("the snapshot reports temporality %v, want %v", got, c.want)
		}
		if got := first.Sums["requests"].Points[0].Value; got != 3 {
			t.Errorf("the first collection reads %d, want 3", got)
		}

		second := m.Collect()
		if got := second.Sums["requests"].Points[0].Value; got != c.wantSecond {
			t.Errorf("the second collection reads %d, want %d", got, c.wantSecond)
		}
		//: the window's start is what tells a reader which of the two it is
		//: holding, so it has to move exactly when the values do.
		moved := !second.StartTime.Equal(first.StartTime)
		if moved != c.startMoves {
			t.Errorf("the window start moved = %v, want %v", moved, c.startMoves)
		}
		//: and the window always closes at or after it opens.
		if second.Time.Before(second.StartTime) {
			t.Errorf("the window ends (%v) before it starts (%v)", second.Time, second.StartTime)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeResourceAndScope pins the two identities a snapshot carries ONCE
// for the whole payload rather than on every point — which is the entire reason
// the OTel model has them.
func TestFacadeResourceAndScope(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		resource    metrics.Resource
		scope       metrics.Scope
		wantService string
		wantScope   string
		wantVersion string
	}
	tests := []tc{
		{
			//: the specification MANDATES unknown_service here, so the clamp
			//: substitutes nobody's judgement (ADR 0031).
			name:        "an unidentified producer takes the mandated default",
			wantService: metrics.UnknownService,
			wantScope:   metrics.DefaultScopeName,
		},
		{
			name: "a declared service name is kept",
			resource: metrics.Resource{Attrs: []metrics.Attr{
				metrics.String(metrics.ServiceNameKey, "orders"),
			}},
			wantService: "orders",
			wantScope:   metrics.DefaultScopeName,
		},
		{
			name:        "a declared scope is kept whole",
			scope:       metrics.Scope{Name: "github.com/acme/orders", Version: "1.4.0"},
			wantService: metrics.UnknownService,
			wantScope:   "github.com/acme/orders",
			wantVersion: "1.4.0",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeterWithConfig(metrics.MeterConfig{Resource: c.resource, Scope: c.scope})
		snap := m.Collect()

		var service string
		for _, a := range snap.Resource.Attrs {
			if a.Key == metrics.ServiceNameKey {
				service = a.Str()
			}
		}
		if service != c.wantService {
			t.Errorf("service.name = %q, want %q", service, c.wantService)
		}
		if snap.Scope.Name != c.wantScope {
			t.Errorf("scope name = %q, want %q", snap.Scope.Name, c.wantScope)
		}
		//: an absent version stays absent — the specification makes it
		//: optional, and inventing one would be a claim about the caller.
		if snap.Scope.Version != c.wantVersion {
			t.Errorf("scope version = %q, want %q", snap.Scope.Version, c.wantVersion)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeObservables pins the asynchronous instruments: a callback read once
// per Collect rather than a handle written to per observation.
//
// Two properties matter and neither is obvious. The callback runs on EVERY
// collection, so a value that changes between two scrapes is reported twice
// with two different numbers. And it reports an ABSOLUTE total, so under delta
// temporality the SDK differences successive reports rather than passing them
// through.
func TestFacadeObservables(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeter()

	reading := int64(10)
	m.ObservableCounter("allocated_total", func(observe metrics.ObserveInt64) {
		observe(reading, metrics.String("arena", "small"))
	})
	m.ObservableUpDownCounter("goroutines", func(observe metrics.ObserveInt64) {
		observe(-3)
	})
	m.ObservableGauge("temperature", func(observe metrics.ObserveFloat64) {
		observe(21.5)
	})

	first := m.Collect()
	if got := first.Sums["allocated_total"].Points[0].Value; got != 10 {
		t.Errorf("the observable counter reads %d, want 10", got)
	}
	//: an observable counter is still a MONOTONIC sum.
	if !first.Sums["allocated_total"].Monotonic {
		t.Error("an ObservableCounter produced a non-monotonic sum")
	}
	//: and an observable up-down counter is still a non-monotonic one, which
	//: is what lets it report a negative absolute at all.
	if first.Sums["goroutines"].Monotonic {
		t.Error("an ObservableUpDownCounter produced a monotonic sum")
	}
	if got := first.Sums["goroutines"].Points[0].Value; got != -3 {
		t.Errorf("the observable up-down counter reads %d, want -3", got)
	}
	if got := first.Gauges["temperature"].Points[0].Value; got != 21.5 {
		t.Errorf("the observable gauge reads %v, want 21.5", got)
	}
	//: the attributes a callback reports name a series exactly as a
	//: synchronous fetch's do.
	if attrs := first.Sums["allocated_total"].Points[0].Attrs; len(attrs) != 1 ||
		attrs[0].Key != "arena" || attrs[0].Str() != "small" {
		t.Errorf("the observed series carries attributes %v, want arena=small", attrs)
	}

	//: the callback is read again on the next collection, which is the whole
	//: difference from a synchronous instrument.
	reading = 25
	if got := m.Collect().Sums["allocated_total"].Points[0].Value; got != 25 {
		t.Errorf("the second collection reads %d, want 25", got)
	}
}

// TestFacadeObservableUnderDelta pins the conversion the SDK does on the
// caller's behalf: an absolute reading becomes the window since the previous
// one, and NOT the absolute value repeated.
func TestFacadeObservableUnderDelta(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeterWithConfig(metrics.MeterConfig{Temporality: metrics.TemporalityDelta})
	readings := []int64{10, 15, 15, 40}
	i := 0
	m.ObservableCounter("allocated_total", func(observe metrics.ObserveInt64) {
		observe(readings[i])
	})

	want := []int64{10, 5, 0, 25}
	for ; i < len(readings); i++ {
		got := m.Collect().Sums["allocated_total"].Points[0].Value
		if got != want[i] {
			t.Errorf("collection %d reads %d, want %d", i+1, got, want[i])
		}
	}
}

// TestFacadeCardinality pins the bound and its zero value through the public
// surface: a caller who never thinks about cardinality is still bounded, and
// one who writes 0 is bounded too (ADR 0031).
func TestFacadeCardinality(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		bound      int
		pushed     int
		wantSeries int
	}
	tests := []tc{
		{name: "a small explicit bound", bound: 3, pushed: 100, wantSeries: 4},
		{name: "under an explicit bound", bound: 100, pushed: 3, wantSeries: 3},
		{
			//: zero is the unset knob, not a request for infinity.
			name: "the zero value clamps to the default", bound: 0, pushed: 10,
			wantSeries: 10,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := metrics.NewMeterWithConfig(metrics.MeterConfig{MaxSeriesPerInstrument: c.bound})
		for i := range c.pushed {
			m.Counter("requests", metrics.String("id", strconv.Itoa(i))).Inc()
		}

		points := m.Collect().Sums["requests"].Points
		if len(points) != c.wantSeries {
			t.Fatalf("%d series, want %d", len(points), c.wantSeries)
		}
		//: every increment landed somewhere, folded or not.
		var total int64
		for _, p := range points {
			total += p.Value
		}
		if total != int64(c.pushed) {
			t.Errorf("the series total %d, want %d", total, c.pushed)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestFacadeOverflowAttrIsReExported pins that a consumer can RECOGNISE the
// overflow condition without importing anything internal. Detecting it is the
// whole reason the fold is visible rather than silent, so the key has to be
// reachable from the same package the meter came from.
func TestFacadeOverflowAttrIsReExported(t *testing.T) {
	t.Parallel()
	m := metrics.NewMeterWithConfig(metrics.MeterConfig{MaxSeriesPerInstrument: 1})
	for i := range 10 {
		m.Counter("requests", metrics.String("id", strconv.Itoa(i))).Inc()
	}

	var overflow *metrics.SumPoint
	for _, p := range m.Collect().Sums["requests"].Points {
		for _, a := range p.Attrs {
			//: the marker is a BOOL now — typed attributes let it be the
			//: thing it always meant instead of the word "true".
			if a.Key == metrics.OverflowAttrKey && a.Kind() == metrics.AttrKindBool && a.Bool() {
				overflow = &p
			}
		}
	}
	if overflow == nil {
		t.Fatal("no overflow series is visible after exceeding the bound")
	}
	//: the aggregated series carries every folded observation, so the metric's
	//: grand total is still right even though its breakdown is gone.
	if overflow.Value != 9 {
		t.Errorf("the overflow series totals %d, want 9", overflow.Value)
	}
	if metrics.DefaultMaxSeriesPerInstrument <= 0 {
		t.Errorf("DefaultMaxSeriesPerInstrument = %d, want a positive bound",
			metrics.DefaultMaxSeriesPerInstrument)
	}
}

// TestFacadeInvalidTemporalityRefuses pins the last half of ADR 0031 on this
// knob: unset CLAMPS, and a value that is none of the three constants — which
// only a deliberate cast can produce — REFUSES.
func TestFacadeInvalidTemporalityRefuses(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a cast temporality did not refuse")
		}
		msg, isString := r.(string)
		if !isString || !strings.Contains(msg, "INVALID_TEMPORALITY") {
			t.Errorf("the panic value is %v, want the InvalidTemporality message", r)
		}
	}()
	metrics.NewMeterWithConfig(metrics.MeterConfig{Temporality: metrics.Temporality(7)})
}
