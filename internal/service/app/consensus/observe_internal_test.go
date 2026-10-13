package consensus

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
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
		_ = tn.node.HardClose()
		_ = c.net.Transport(tn.id).Close()
	}
	for i, tn := range c.nodes {
		tn.sm = newKV()
		cfg := c.config(tn)
		if i == 0 {
			cfg.Meter, cfg.Tracer, cfg.Logger = meter, tr, lg
		}
		n, err := NewRaft(cfg)
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
	_ = n0.UpdateAddress(ctx, "op:test", c.nodes[1].id, "elsewhere")
	time.Sleep(300 * time.Millisecond)
	snap := meter.Collect()
	for _, name := range []string{mRole, mTerm, mHasLeader, mApplyLagEntries, mClusterVersion} {
		if _, ok := snap.Gauges[name]; !ok {
			t.Errorf("gauge %s missing", name)
		}
	}
	for _, name := range []string{mFsyncLatency, mCommitLatency, mBarrierLatency, mLoopTurn, mApplyLatency} {
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
