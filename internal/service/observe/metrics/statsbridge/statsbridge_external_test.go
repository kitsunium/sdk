package statsbridge_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcdocstore "github.com/kitsunium/sdk/internal/service/data/docstore"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
	"github.com/kitsunium/sdk/internal/service/observe/metrics/statsbridge"
)

// doc is the docstore's test document.
type doc struct{ ID string }

// engines builds one of each engine the bridge covers, moves each counter,
// and returns their observers.
func engines(t *testing.T) []coremetrics.Observer {
	t.Helper()
	c := kcache.NewCache(kcache.Config[string, int]{MaxEntries: 1})
	c.Set("a", 1)
	c.Fetch("a")
	c.Fetch("missing")
	c.Set("b", 2) // evicts a
	store, err := svcdocstore.Open(svcdocstore.Config[doc]{Key: func(d doc) string { return d.ID }})
	if err != nil {
		t.Fatalf("docstore.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{"x", "y"} {
		if err := store.Put(doc{ID: id}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Policy: corequeue.PolicyValue{VisibilityTimeout: 1 << 40, MaxDeliveries: 10, MaxMessageBytes: 1 << 20}})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	for range 3 {
		if _, err := broker.Publish(context.Background(), []byte("m")); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	forwarder, err := svcforward.New(context.Background(), svcforward.Config{
		Broker: broker, Send: func(context.Context, []byte) error { return nil }, Retryable: func(error) bool { return false },
	})
	if err != nil {
		t.Fatalf("forward.New: %v", err)
	}
	if err := forwarder.Enqueue(context.Background(), []byte("batch")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	counter, ok := broker.(corequeue.Counter)
	if !ok {
		t.Fatal("the memory broker is not a Counter")
	}
	return []coremetrics.Observer{
		statsbridge.Cache("sessions", c.Stats),
		statsbridge.Docstore("", store.Stats),
		statsbridge.Forward("spans", forwarder.Stats),
		statsbridge.Queue("jobs", counter, 0),
	}
}

// TestEveryEngineReachesTheMeter (#871 acceptance): the cache, the docstore,
// the forwarder and the queue broker each reach a meter through
// RegisterObservers with their names, kinds, units and values; and the
// Prometheus exporter reads every one of them.
func TestEveryEngineReachesTheMeter(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	svcmetrics.RegisterObservers(m, svcmetrics.ObserverConfig{}, engines(t)...)
	snap := m.Collect()
	wantSums := map[string]struct {
		value     int64
		monotonic bool
		unit      string
		instance  string
	}{
		"kitsunium.cache.hits":              {1, true, "{lookup}", "sessions"},
		"kitsunium.cache.misses":            {1, true, "{lookup}", "sessions"},
		"kitsunium.cache.evictions":         {1, true, "{entry}", "sessions"},
		"kitsunium.docstore.documents":      {2, false, "{document}", ""},
		"kitsunium.forward.pending":         {4, false, "{batch}", "spans"},
		"kitsunium.forward.enqueued":        {1, true, "{batch}", "spans"},
		"kitsunium.queue.queued":            {4, false, "{message}", "jobs"},
		"kitsunium.queue.dead":              {0, false, "{message}", "jobs"},
		"kitsunium.docstore.versions.bytes": {0, false, "By", ""},
	}
	for name, want := range wantSums {
		sum, ok := snap.Sums[name]
		if !ok || len(sum.Points) != 1 {
			t.Fatalf("%s: not in the snapshot", name)
		}
		point := sum.Points[0]
		if point.Value != want.value || sum.Monotonic != want.monotonic || sum.Unit != want.unit {
			t.Errorf("%s = %d (monotonic %v, unit %q), want %d (%v, %q)", name, point.Value, sum.Monotonic, sum.Unit, want.value, want.monotonic, want.unit)
		}
		if got := instanceOf(point); got != want.instance {
			t.Errorf("%s: instance %q, want %q", name, got, want.instance)
		}
	}
	underscored := svcmetrics.NewMeter()
	svcmetrics.RegisterObservers(underscored, svcmetrics.ObserverConfig{Underscore: true}, engines(t)...)
	var prom bytes.Buffer
	if err := svcmetrics.NewPrometheusExporter("p", &prom).Export(underscored.Collect()); err != nil {
		t.Fatalf("Prometheus: %v", err)
	}
	for _, line := range []string{
		`kitsunium_cache_hits{kitsunium_instance="sessions"} 1`,
		`kitsunium_docstore_documents 2`,
		`kitsunium_forward_enqueued{kitsunium_instance="spans"} 1`,
		`kitsunium_queue_queued{kitsunium_instance="jobs"} 4`,
	} {
		if !strings.Contains(prom.String(), line+"\n") {
			t.Errorf("the exposition lacks %q:\n%s", line, prom.String())
		}
	}
}

// instanceOf is the point's InstanceKey value, "" when it has none.
func instanceOf(point coremetrics.SumValue) string {
	for _, attr := range point.Attrs {
		if attr.Key == statsbridge.InstanceKey {
			return attr.Str()
		}
	}
	return ""
}

// countingObserver counts its Observe calls and reports two statistics, one
// with no kind.
type countingObserver struct{ calls int }

// Observe reports a counter, a gauge, and a kind-less statistic.
func (o *countingObserver) Observe(report coremetrics.ReportStat) {
	o.calls++
	report("x.count", coremetrics.StatCounter, "{x}", int64(o.calls))
	report("x.level", coremetrics.StatGauge, "", 7)
	report("x.nothing", 0, "", 1)
}

// TestAnObserverIsReadOncePerCollection: the registration reads it once,
// each collection once more whatever it reports; a statistic with no kind
// is dropped; the unit is bound.
func TestAnObserverIsReadOncePerCollection(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	o := &countingObserver{}
	svcmetrics.RegisterObservers(m, svcmetrics.ObserverConfig{}, o)
	if o.calls != 1 {
		t.Fatalf("the registration read the observer %d times, want 1", o.calls)
	}
	m.Collect()
	snap := m.Collect()
	if o.calls != 3 {
		t.Fatalf("two collections read the observer %d times in all, want 3", o.calls)
	}
	if got := snap.Sums["x.count"]; got.Points[0].Value != 3 || got.Unit != "{x}" || !got.Monotonic {
		t.Errorf("x.count = %+v", got)
	}
	if _, ok := snap.Sums["x.nothing"]; ok {
		t.Error("a statistic with no kind reached the meter")
	}
	if snap.Sums["x.level"].Points[0].Value != 7 || snap.Sums["x.level"].Monotonic {
		t.Errorf("x.level = %+v", snap.Sums["x.level"])
	}
}
