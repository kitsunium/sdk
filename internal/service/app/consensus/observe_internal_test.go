package consensus

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

type fakeSpan struct {
	mu     *sync.Mutex
	events *[]string
}

func (s fakeSpan) SpanContext() coretrace.SpanContextValue       { return coretrace.SpanContextValue{} }
func (s fakeSpan) SetAttrs(...coreotel.AttrValue)                {}
func (s fakeSpan) SetStatus(coretrace.StatusCode, string)        {}
func (s fakeSpan) AddEvent(name string, _ ...coreotel.AttrValue) { s.add("event:" + name) }
func (s fakeSpan) End()                                          { s.add("end") }
func (s fakeSpan) add(e string)                                  { s.mu.Lock(); *s.events = append(*s.events, e); s.mu.Unlock() }

type fakeTracer struct {
	mu     sync.Mutex
	events []string
}

func (t *fakeTracer) Start(ctx context.Context, name string, _ coretrace.SpanParams) (context.Context, coretrace.Span) {
	t.mu.Lock()
	t.events = append(t.events, "start:"+name)
	t.mu.Unlock()
	return ctx, fakeSpan{mu: &t.mu, events: &t.events}
}

type fakeLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *fakeLogger) Log(_ context.Context, lv level.Level, msg string, _ ...corelogger.AttrValue) {
	l.mu.Lock()
	l.msgs = append(l.msgs, lv.String()+" "+msg)
	l.mu.Unlock()
}
func (l *fakeLogger) With(...corelogger.AttrValue) corelogger.Logger { return l }
func (l *fakeLogger) WithGroup(string) corelogger.Logger             { return l }
func (l *fakeLogger) Enabled(context.Context, level.Level) bool      { return true }

// D13: the contract's metric names, one span per Propose, and the role,
// membership and administration events.
func TestObservability(t *testing.T) {
	meter := svcmetrics.NewMeter()
	tr := &fakeTracer{}
	lg := &fakeLogger{}
	c := newClusterWith(t, &cluster{t: t, net: NewMemoryNetwork(), cid: coreconsensus.ClusterID{9, 9}, every: 0})
	for _, tn := range c.nodes {
		if err := tn.node.HardClose(); err != nil {
			t.Fatalf("hard close %d: %v", tn.id, err)
		}
		if err := NewMemoryTransport(c.net, tn.id).Close(); err != nil {
			t.Fatalf("close transport %d: %v", tn.id, err)
		}
	}
	for i, tn := range c.nodes {
		tn.sm = newKV()
		cfg := c.config(tn)
		if i == 0 {
			cfg.Meter, cfg.Tracer, cfg.Logger = meter, tr, lg
		}
		n, err := NewNode(cfg)
		if err != nil {
			t.Fatal(err)
		}
		tn.node = n
	}
	c.leader(5 * time.Second)
	n0 := c.nodes[0].node
	propose(t, n0, "a=1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := n0.Barrier(ctx); err != nil {
		t.Fatal(err)
	}
	if err := n0.UpdateAddress(ctx, "op:test", c.nodes[1].id, "elsewhere"); err != nil {
		t.Fatalf("update address: %v", err)
	}
	gauges := []string{mRole, mTerm, mHasLeader, mApplyLagEntries, mClusterVersion}
	histograms := []string{mFsyncLatency, mCommitLatency, mBarrierLatency, mLoopTurn, mApplyLatency}
	// The node publishes on its own goroutines: wait until every series,
	// span event and log line has been published — a state observed, not a
	// pause guessed — then hold each to its shape below.
	deadline := time.Now().Add(5 * time.Second)
	for !observed(meter, tr, lg, gauges, histograms) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snap := meter.Collect()
	for _, name := range gauges {
		if _, ok := snap.Gauges[name]; !ok {
			t.Errorf("gauge %s missing", name)
		}
	}
	for _, name := range histograms {
		if _, ok := snap.Histograms[name]; !ok {
			t.Errorf("histogram %s missing", name)
		}
	}
	for name := range snap.Sums {
		if !strings.HasSuffix(name, "_total") {
			t.Errorf("counter %s does not end in _total", name)
		}
	}
	tr.mu.Lock()
	ev := strings.Join(tr.events, ",")
	tr.mu.Unlock()
	if !strings.Contains(ev, "start:consensus.Propose") || !strings.Contains(ev, "event:applied") || !strings.Contains(ev, "end") {
		t.Errorf("spans: %s", ev)
	}
	lg.mu.Lock()
	logs := strings.Join(lg.msgs, "\n")
	lg.mu.Unlock()
	for _, want := range []string{"role or term changed", "administrative operation"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log %q missing in:\n%s", want, logs)
		}
	}
}

// observed reports whether node 0 has published everything TestObservability
// holds to its shape: each gauge and histogram named, its spans' events and
// both log lines.
func observed(meter coremetrics.FullMeter, tr *fakeTracer, lg *fakeLogger, gauges, histograms []string) bool {
	snap := meter.Collect()
	for _, name := range gauges {
		if _, ok := snap.Gauges[name]; !ok {
			return false
		}
	}
	for _, name := range histograms {
		if _, ok := snap.Histograms[name]; !ok {
			return false
		}
	}
	tr.mu.Lock()
	ev := strings.Join(tr.events, ",")
	tr.mu.Unlock()
	if !strings.Contains(ev, "start:consensus.Propose") || !strings.Contains(ev, "event:applied") || !strings.Contains(ev, "end") {
		return false
	}
	lg.mu.Lock()
	logs := strings.Join(lg.msgs, "\n")
	lg.mu.Unlock()
	return strings.Contains(logs, "role or term changed") && strings.Contains(logs, "administrative operation")
}

// D13: certificate expiry over the TCP transport, log bytes, quota and free
// space over the data directory.
func TestResourceMetrics(t *testing.T) {
	_, cid, ns := newTCPCluster(t)
	tcpLeader(t, ns)
	tn := ns[0]
	if err := tn.node.HardClose(); err != nil {
		t.Fatal(err)
	}
	meter := svcmetrics.NewMeter()
	tn.sm = newKV()
	cfg := tcpConfig(cid, tn)
	cfg.Meter = meter
	n, err := NewNode(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tn.node = n
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := meter.Collect()
		_, own := snap.Gauges[mCertExpiry]
		_, peer := snap.Gauges[mPeerCertExpiry]
		_, quota := snap.Gauges[mLogQuota]
		if own && peer && quota {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("certificate gauges missing: own %v peer %v quota %v", own, peer, quota)
		}
		time.Sleep(50 * time.Millisecond)
	}

	c, nodes := threeOnDisk(t, 5)
	dn := nodes[0]
	dn.crash(t, c)
	dn.dd = openDir(t, dn.dir, 16<<10)
	dn.sm = newKV()
	dcfg := diskConfig(c, dn)
	dm := svcmetrics.NewMeter()
	dcfg.Meter = dm
	if dn.node, err = NewNode(dcfg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, x := range nodes {
			if x.node != nil {
				x.crash(t, c)
			}
		}
	}()
	// The free-space gauge is published where the platform can measure the
	// filesystem, and only there: a -1 is never reported as a size.
	wantFree := fsFree(dn.dir) >= 0
	deadline = time.Now().Add(5 * time.Second)
	for {
		snap := dm.Collect()
		lb, ok1 := snap.Gauges[mLogBytes]
		_, ok2 := snap.Gauges[mFsFree]
		if ok1 && ok2 == wantFree && len(lb.Points) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("log gauges: bytes %v, free %v (want free %v on %s)", ok1, ok2, wantFree, runtime.GOOS)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
