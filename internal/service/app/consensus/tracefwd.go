package consensus

import (
	"context"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	"github.com/kitsunium/sdk/internal/service/app/consensus/raft"
)

// traceAppended records the "appended" event of a traced proposal at its
// position, then "committed" at once when the commit already covers it, or
// keeps it for the commit that will.
func (n *Node) traceAppended(p *proposal, pos coreconsensus.PositionValue) {
	spanEvent(p.span, "appended", coreotel.Int64("index", int64(pos.Index)), coreotel.Int64("term", int64(pos.Term)))
	if pos.Index <= n.traced.commit {
		spanEvent(p.span, "committed", coreotel.Int64("index", int64(pos.Index)))
		return
	}
	n.traced.waits = append(n.traced.waits, p)
}

// traceForwardFlags returns the flag and the trace context a forward of p
// carries: its span's context once the committed cluster version is 4,
// which every member announced, and nothing otherwise, so a node of
// protocol 3 never meets the trailing bytes.
func traceForwardFlags(p *proposal, cluster uint32) (coreconsensus.MessageFlags, coreconsensus.TraceContextValue) {
	if p.span == nil || cluster < raft.Protocol4 {
		return 0, coreconsensus.TraceContextValue{}
	}
	sc := p.span.SpanContext()
	if !sc.IsValid() {
		return 0, coreconsensus.TraceContextValue{}
	}
	return coreconsensus.FlagTrace, coreconsensus.TraceContextValue{TraceID: sc.TraceID, SpanID: sc.SpanID, Sampled: sc.IsSampled()}
}

// traceForward opens, on the node that handles a traced forward, the
// "consensus.Forward" span whose parent is the proposer's span, so the two
// are one trace. A forward sent again under the same id while its first
// copy waits for its answer opens nothing.
func (n *Node) traceForward(m *coreconsensus.MessageValue) {
	if m.Flags&coreconsensus.FlagTrace == 0 || n.obs.tracer == nil {
		return
	}
	key := fwdTraceKey{from: m.From, ctx: m.Context}
	if _, ok := n.traced.answers[key]; ok {
		return
	}
	parent := coretrace.SpanContextValue{
		TraceID: coretrace.TraceID(m.Trace.TraceID), SpanID: coretrace.SpanID(m.Trace.SpanID),
		Flags: coretrace.TraceFlags(0).WithSampled(m.Trace.Sampled), Remote: true,
	}
	span := n.obs.span(coretrace.ContextWithSpanContext(context.Background(), parent), "consensus.Forward")
	if n.traced.answers == nil {
		n.traced.answers = map[fwdTraceKey]coretrace.Span{}
	}
	n.traced.answers[key] = span
}

// traceReady carries a Ready's part of the traces, before its entries are
// handed to apply: the answers to traced forwards it sends, then the
// commit it moves.
func (n *Node) traceReady(rd *raft.ReadyValue) {
	if len(n.traced.answers) > 0 {
		n.traceAnswers(rd.Messages)
		n.traceAnswers(rd.AfterSync)
	}
	if rd.Commit <= n.traced.commit {
		return
	}
	n.traced.commit = rd.Commit
	if len(n.traced.waits) > 0 || len(n.traced.open) > 0 {
		n.traceCommitted()
	}
}

// traceAnswers takes the position, or the refusal, each traced forward was
// answered with.
func (n *Node) traceAnswers(ms []coreconsensus.MessageValue) {
	for i := range ms {
		m := &ms[i]
		if m.Kind != coreconsensus.MsgForwardResp && m.Kind != coreconsensus.MsgForwardAdminResp {
			continue
		}
		key := fwdTraceKey{from: m.To, ctx: m.Context}
		span, ok := n.traced.answers[key]
		if !ok {
			continue
		}
		delete(n.traced.answers, key)
		if m.Reject {
			spanEnd(span, sentinelOf(m.Code))
			continue
		}
		spanEvent(span, "appended", coreotel.Int64("index", int64(m.Index)), coreotel.Int64("term", int64(m.LogTerm)))
		n.traced.open = append(n.traced.open, fwdSpan{index: m.Index, span: span})
	}
}

// traceCommitted records "committed" on every waiting proposal the commit
// covers, and ends every forward span it covers.
func (n *Node) traceCommitted() {
	c := n.traced.commit
	k := 0
	for _, p := range n.traced.waits {
		if p.pos.Index <= c {
			spanEvent(p.span, "committed", coreotel.Int64("index", int64(p.pos.Index)))
			continue
		}
		n.traced.waits[k] = p
		k++
	}
	clear(n.traced.waits[k:])
	n.traced.waits = n.traced.waits[:k]
	k = 0
	for _, f := range n.traced.open {
		if f.index <= c {
			spanEvent(f.span, "committed", coreotel.Int64("index", int64(f.index)))
			spanEnd(f.span, nil)
			continue
		}
		n.traced.open[k] = f
		k++
	}
	clear(n.traced.open[k:])
	n.traced.open = n.traced.open[:k]
}

// traceEnd ends every forward span still open with err, the leadership
// lost or the node stopped: the position it answered may never commit.
// The proposals' own spans end with their callers' answers.
func (n *Node) traceEnd(err error) {
	for key, span := range n.traced.answers {
		delete(n.traced.answers, key)
		spanEnd(span, err)
	}
	for _, f := range n.traced.open {
		spanEnd(f.span, err)
	}
	clear(n.traced.open)
	n.traced.open = n.traced.open[:0]
}
