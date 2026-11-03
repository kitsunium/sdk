package statsbridge

import (
	"context"
	"time"

	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kcache "github.com/kitsunium/sdk/internal/kernel/collections/cache"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcdocstore "github.com/kitsunium/sdk/internal/service/data/docstore"
)

// InstanceKey is the attribute each adapter stamps its statistics with when
// given a name, so two caches — two queues — are two series of one metric.
// It is spelled with '_' so the Prometheus connector, which refuses a dotted
// label name, carries it as OTLP does.
const InstanceKey string = "kitsunium_instance"

// The units the adapters report in (UCUM).
const (
	unitLookup   string = "{lookup}"
	unitEntry    string = "{entry}"
	unitWeight   string = "{weight}"
	unitDocument string = "{document}"
	unitFold     string = "{fold}"
	unitVersion  string = "{version}"
	unitBytes    string = "By"
	unitBatch    string = "{batch}"
	unitMessage  string = "{message}"
	unitMillis   string = "ms"
)

// instance is the attribute set of name: InstanceKey=name, or none. Built
// once, so every report reuses it without an allocation.
func instance(name string) []coreotel.AttrValue {
	//: no name, no attribute: the dimensionless series.
	if name == "" {
		return nil
	}
	//: one attribute, held for the observer's life.
	return []coreotel.AttrValue{coreotel.String(InstanceKey, name)}
}

// cacheObserver reports a kernel cache's Stats.
type cacheObserver struct {
	stats func() kcache.StatsValue
	attrs []coreotel.AttrValue
}

// Cache adapts a kernel cache's Stats — Cache.Stats or Striped.Stats, bound —:
// kitsunium.cache.hits, .misses and .evictions (counters, {lookup} and
// {entry}) and kitsunium.cache.weight (a gauge, {weight}), stamped
// InstanceKey=name when name is not empty.
func Cache(name string, stats func() kcache.StatsValue) coremetrics.Observer {
	//: the attribute set built once.
	return &cacheObserver{stats: stats, attrs: instance(name)}
}

// Observe implements coremetrics.Observer.
func (o *cacheObserver) Observe(report coremetrics.ReportStat) {
	s := o.stats()
	report("kitsunium.cache.hits", coremetrics.StatCounter, unitLookup, clampUint(s.Hits), o.attrs...)
	report("kitsunium.cache.misses", coremetrics.StatCounter, unitLookup, clampUint(s.Misses), o.attrs...)
	report("kitsunium.cache.evictions", coremetrics.StatCounter, unitEntry, clampUint(s.Evictions), o.attrs...)
	report("kitsunium.cache.weight", coremetrics.StatGauge, unitWeight, s.Weight, o.attrs...)
}

// docstoreObserver reports a docstore's Stats.
type docstoreObserver struct {
	stats func() svcdocstore.StatsValue
	attrs []coreotel.AttrValue
}

// Docstore adapts a docstore's Stats — Store.Stats, bound —:
// kitsunium.docstore.documents and .pending (gauges, {document} and
// {entry}), .folds (a counter, {fold}), .versions.former, .versions.trashed
// (gauges, {version} and {document}) and .versions.bytes (a gauge, By),
// stamped InstanceKey=name when name is not empty.
func Docstore(name string, stats func() svcdocstore.StatsValue) coremetrics.Observer {
	//: the attribute set built once.
	return &docstoreObserver{stats: stats, attrs: instance(name)}
}

// Observe implements coremetrics.Observer.
func (o *docstoreObserver) Observe(report coremetrics.ReportStat) {
	s := o.stats()
	report("kitsunium.docstore.documents", coremetrics.StatGauge, unitDocument, int64(s.Documents), o.attrs...)
	report("kitsunium.docstore.pending", coremetrics.StatGauge, unitEntry, int64(s.Pending), o.attrs...)
	report("kitsunium.docstore.folds", coremetrics.StatCounter, unitFold, int64(s.Folds), o.attrs...)
	report("kitsunium.docstore.versions.former", coremetrics.StatGauge, unitVersion, int64(s.Versions.Former), o.attrs...)
	report("kitsunium.docstore.versions.trashed", coremetrics.StatGauge, unitDocument, int64(s.Versions.Trashed), o.attrs...)
	report("kitsunium.docstore.versions.bytes", coremetrics.StatGauge, unitBytes, s.Versions.Bytes, o.attrs...)
}

// forwardObserver reports a forwarder's Stats.
type forwardObserver struct {
	stats func() svcforward.StatsValue
	attrs []coreotel.AttrValue
}

// Forward adapts a forwarder's Stats — Forwarder.Stats, bound —:
// kitsunium.forward.pending ({batch}) and .pending_bytes (By), gauges; and
// .enqueued, .sent, .retries, .dropped, .expired and .dead_lettered, counters
// of {batch} — stamped InstanceKey=name when name is not empty.
func Forward(name string, stats func() svcforward.StatsValue) coremetrics.Observer {
	//: the attribute set built once.
	return &forwardObserver{stats: stats, attrs: instance(name)}
}

// Observe implements coremetrics.Observer.
func (o *forwardObserver) Observe(report coremetrics.ReportStat) {
	s := o.stats()
	report("kitsunium.forward.pending", coremetrics.StatGauge, unitBatch, int64(s.Pending), o.attrs...)
	report("kitsunium.forward.pending_bytes", coremetrics.StatGauge, unitBytes, s.PendingBytes, o.attrs...)
	report("kitsunium.forward.enqueued", coremetrics.StatCounter, unitBatch, clampUint(s.Enqueued), o.attrs...)
	report("kitsunium.forward.sent", coremetrics.StatCounter, unitBatch, clampUint(s.Sent), o.attrs...)
	report("kitsunium.forward.retries", coremetrics.StatCounter, unitBatch, clampUint(s.Retries), o.attrs...)
	report("kitsunium.forward.dropped", coremetrics.StatCounter, unitBatch, clampUint(s.Dropped), o.attrs...)
	report("kitsunium.forward.expired", coremetrics.StatCounter, unitBatch, clampUint(s.Expired), o.attrs...)
	report("kitsunium.forward.dead_lettered", coremetrics.StatCounter, unitBatch, clampUint(s.DeadLettered), o.attrs...)
}

// queueObserver reports a broker's Counter.
type queueObserver struct {
	counter corequeue.Counter
	timeout time.Duration
	attrs   []coreotel.AttrValue
	// clk reads the oldest message's age.
	clk clock.Clock
	// described is set after the first Observe, the registration's: that
	// one reports every statistic even when the count fails, so the
	// instruments exist from the start.
	described bool
}

// Queue adapts a broker's Counter: kitsunium.queue.queued, .leased and .dead
// (gauges, {message}) and .oldest_age (a gauge in ms, 0 when the queue is
// empty), stamped InstanceKey=name when name is not empty. Counter.Stats is
// the broker's own read — a query on SQL, a directory walk on file — paid
// once per collection and bounded by timeout; zero or less reads under no
// deadline, which a memory broker needs and which allocates nothing, where
// a deadline costs the context's few allocations per collection. A read
// that fails or runs out reports nothing for that collection.
func Queue(name string, counter corequeue.Counter, timeout time.Duration) coremetrics.Observer {
	//: the attribute set built once.
	return &queueObserver{counter: counter, timeout: timeout, attrs: instance(name), clk: clock.System}
}

// Observe implements coremetrics.Observer. The registration's describing
// call reports every statistic even when the count fails, so the
// instruments exist from the start; a collection whose count fails reports
// nothing.
func (o *queueObserver) Observe(report coremetrics.ReportStat) {
	s, err := o.read()
	//: no count this time: nothing to say, but to the registration, which
	//: needs the names.
	if err != nil && o.described {
		return
	}
	o.described = true
	var age int64
	//: an empty queue has no oldest message.
	if !s.OldestEnqueuedAt.IsZero() {
		age = max(o.clk.Now().Sub(s.OldestEnqueuedAt).Milliseconds(), 0)
	}
	report("kitsunium.queue.queued", coremetrics.StatGauge, unitMessage, s.Queued, o.attrs...)
	report("kitsunium.queue.leased", coremetrics.StatGauge, unitMessage, s.Leased, o.attrs...)
	report("kitsunium.queue.dead", coremetrics.StatGauge, unitMessage, s.Dead, o.attrs...)
	report("kitsunium.queue.oldest_age", coremetrics.StatGauge, unitMillis, age, o.attrs...)
}

// read counts the queue, under the timeout when there is one.
func (o *queueObserver) read() (corequeue.StatsValue, error) {
	//: no bound: the broker's own pace, and no allocation.
	if o.timeout <= 0 {
		return o.counter.Stats(context.Background())
	}
	ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
	defer cancel()
	//: bounded.
	return o.counter.Stats(ctx)
}

// clampUint is n as an int64, held at the largest one.
func clampUint(n uint64) int64 {
	//: past int64 a counter has wrapped nothing; it is held.
	return int64(min(n, 1<<63-1))
}
