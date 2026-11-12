package consensus

import (
	"slices"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// forwardTap is a memory transport that keeps a copy of every forward
// its node sends.
type forwardTap struct {
	*MemoryTransport
	mu       sync.Mutex
	forwards [][]byte
}

func (t *forwardTap) Send(to coreconsensus.NodeID, frame []byte) {
	if len(frame) > 5 && coreconsensus.MessageKind(frame[5]) == coreconsensus.MsgForward {
		t.mu.Lock()
		t.forwards = append(t.forwards, append([]byte(nil), frame...))
		t.mu.Unlock()
	}
	t.MemoryTransport.Send(to, frame)
}

// taken returns the forwards sent since the last call.
func (t *forwardTap) taken() [][]byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.forwards
	t.forwards = nil
	return f
}

// tracedNode is a test node's recorder and tap.
type tracedNode struct {
	rec *svctrace.Recorder
	tap *forwardTap
}

// D13: a Propose submitted on a follower is one trace across the two nodes
// once the cluster version is 4 — the follower's consensus.Propose with its
// appended, committed and applied events, and the leader's
// consensus.Forward, its child, through the trace context the forward
// carries. At cluster version 3 the forward carries no trace bytes, the
// layout a node of protocol 3 parses, and the leader traces nothing of it
// (#1115).
func TestAForwardedProposeIsOneTraceAcrossTheTwoNodes(t *testing.T) {
	var mu sync.Mutex
	traced := map[*testNode]*tracedNode{}
	net := NewMemoryNetwork()
	c := newClusterWith(t, &cluster{t: t, net: net, cid: coreconsensus.ClusterID{11, 15}, configure: func(tn *testNode, cfg *Config) {
		mu.Lock()
		defer mu.Unlock()
		tr, ok := traced[tn]
		if !ok {
			tr = &tracedNode{rec: svctrace.NewRecorder(svctrace.RecorderConfig{}), tap: &forwardTap{MemoryTransport: NewMemoryTransport(net, tn.id)}}
			traced[tn] = tr
		}
		cfg.Tracer = svctrace.NewTracer(svctrace.TracerConfig{Sink: tr.rec.Sink()})
		cfg.Transport = tr.tap
	}})
	nodes := make([]*Node, 0, len(c.nodes))
	for _, tn := range c.nodes {
		nodes = append(nodes, tn.node)
	}
	raiseNodesTo(t, nodes, raft.Protocol3)

	l := c.settledLeader(5 * time.Second)
	f := c.otherThan(l)
	for _, tr := range traced {
		tr.rec.Collect()
		tr.tap.taken()
	}
	propose(t, f.node, "a=1")
	frames := traced[f].tap.taken()
	if len(frames) == 0 {
		t.Fatal("version 3: the follower forwarded nothing")
	}
	for _, b := range frames {
		var m coreconsensus.MessageValue
		if err := wire.ParseMessage(b, &m); err != nil {
			t.Fatalf("version 3: %v", err)
		}
		// The frozen header, the fixed body, one entry's header and its
		// payload: the layout of protocol 3, byte for byte.
		if m.Flags&coreconsensus.FlagTrace != 0 || len(b) != 14+98+33+len(m.Entries[0].Data) {
			t.Fatalf("version 3: a forward of %d bytes, flags %#x", len(b), m.Flags)
		}
	}
	if s := named(traced[l].rec.Collect().Spans, "consensus.Forward"); len(s) != 0 {
		t.Fatalf("version 3: the leader traced %d forwards", len(s))
	}

	raiseNodesTo(t, nodes, raft.Protocol4)
	l = c.settledLeader(5 * time.Second)
	f = c.otherThan(l)
	for _, tr := range traced {
		tr.rec.Collect()
		tr.tap.taken()
	}
	propose(t, f.node, "b=2")
	if b := traced[f].tap.taken(); len(b) == 0 || b[len(b)-1][14]&byte(coreconsensus.FlagTrace) == 0 {
		t.Fatalf("version 4: the forward carries no trace context (%d frames)", len(b))
	}
	ok := named(traced[f].rec.Collect().Spans, "consensus.Propose")
	ok = slices.DeleteFunc(ok, func(s coretrace.SpanValue) bool { return s.Status.Code == coretrace.StatusError })
	if len(ok) != 1 {
		t.Fatalf("version 4: %d successful Propose spans on the follower", len(ok))
	}
	prop := ok[0]
	if got := eventNames(prop); !slices.Equal(got, []string{"appended", "committed", "applied"}) {
		t.Fatalf("version 4: the Propose span's events are %v", got)
	}
	var all []coretrace.SpanValue
	waitFor(t, "the leader's Forward span ends", func() bool {
		for _, tr := range traced {
			all = append(all, tr.rec.Collect().Spans...)
		}
		return len(named(all, "consensus.Forward")) > 0
	})
	all = append(all, prop)
	same := slices.DeleteFunc(all, func(s coretrace.SpanValue) bool { return s.Context.TraceID != prop.Context.TraceID })
	if len(same) != 2 {
		t.Fatalf("version 4: %d spans in the trace, want 2: %+v", len(same), same)
	}
	fwd := named(same, "consensus.Forward")
	if len(fwd) != 1 || fwd[0].Parent.SpanID != prop.Context.SpanID || !fwd[0].Parent.Remote {
		t.Fatalf("version 4: the leader's span is not the follower's child: %+v", fwd)
	}
	if got := eventNames(fwd[0]); !slices.Equal(got, []string{"appended", "committed"}) {
		t.Fatalf("version 4: the Forward span's events are %v", got)
	}
}

// named returns the spans of name.
func named(spans []coretrace.SpanValue, name string) []coretrace.SpanValue {
	var out []coretrace.SpanValue
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

// eventNames returns a span's events by name, in order.
func eventNames(s coretrace.SpanValue) []string {
	out := make([]string, 0, len(s.Events))
	for _, e := range s.Events {
		out = append(out, e.Name)
	}
	return out
}
