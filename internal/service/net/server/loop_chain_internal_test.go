// Package server — loop middlewares (UseLoop): the chain served on every
// backend, the layers that fail Start, the honoured options the chain merges,
// and the lane count every backend tells the chain at OnStart.
package server

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// traceLayer records, under its name, every OnOpen and OnData it forwards,
// and the lane count OnStart gave it.
type traceLayer struct {
	corenet.LoopLayer
	name  string
	trace *chainTrace
}

// chainTrace is what the layers of one chain recorded, in order.
type chainTrace struct {
	mu     sync.Mutex
	events []string
	lanes  []int
}

func (t *chainTrace) add(e string) {
	t.mu.Lock()
	t.events = append(t.events, e)
	t.mu.Unlock()
}

func (l *traceLayer) OnStart(lanes int) {
	l.trace.mu.Lock()
	l.trace.lanes = append(l.trace.lanes, lanes)
	l.trace.mu.Unlock()
	l.LoopLayer.OnStart(lanes)
}

func (l *traceLayer) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	l.trace.add(l.name + ".open")
	l.LoopLayer.OnOpen(io, ref)
}

func (l *traceLayer) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	l.trace.add(l.name + ".data")
	return l.LoopLayer.OnData(io, ref, buf)
}

// traceMW is a loop middleware building a traceLayer named name.
func traceMW(name string, t *chainTrace) corenet.Middleware[corenet.LoopHandler] {
	return func(next corenet.LoopHandler) corenet.LoopHandler {
		return &traceLayer{LoopLayer: corenet.NewLoopLayer(next), name: name, trace: t}
	}
}

// startedH is a loopLineH that records the lane count OnStart gave it and
// whether any callback came before.
type startedH struct {
	loopLineH
	lanes, early int
}

func (h *startedH) OnStart(lanes int) {
	h.mu.Lock()
	h.lanes = lanes
	h.mu.Unlock()
}

func (h *startedH) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.mu.Lock()
	if h.lanes == 0 {
		h.early++
	}
	h.mu.Unlock()
	h.loopLineH.OnOpen(io, ref)
}

// TestLoopChain_serves pins UseLoop on every backend: the middlewares are
// built twice at Start, over the probe and over the handler, the layers see
// each callback outermost first, the handler's echo still comes back, and OnStart
// reaches the handler through the chain with the group's lane count before
// any other callback — on the portable backend as on a native one.
func TestLoopChain_serves(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			tr := &chainTrace{}
			h := &startedH{}
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: b, Loops: 2})).
				UseLoop(traceMW("a", tr), nil).UseLoop(traceMW("b", tr)).HandleLoop(h)
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			c := loopDial(t, srv.State().Listeners[0].Address)
			_, _ = c.Write([]byte("hi\n"))
			got := make([]byte, 3)
			if _, err := io.ReadFull(c, got); err != nil || string(got) != "hi\n" {
				t.Fatalf("echo = %q, %v", got, err)
			}
			tr.mu.Lock()
			events, lanes := slices.Clone(tr.events), slices.Clone(tr.lanes)
			tr.mu.Unlock()
			//: the probe chain's layers first, at Start (one lane, the zero
			//: reference), then the served chain's.
			want := []string{"a.open", "b.open", "a.data", "b.data", "a.open", "b.open", "a.data", "b.data"}
			if !slices.Equal(events, want) {
				t.Fatalf("layers saw %v, want %v", events, want)
			}
			if !slices.Equal(lanes, []int{1, 1, 2, 2}) {
				t.Fatalf("layers' OnStart = %v, want [1 1 2 2]", lanes)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.lanes != 2 || h.early != 0 {
				t.Fatalf("handler OnStart = %d, %d callbacks before it", h.lanes, h.early)
			}
		})
	}
}

// opaqueLayer serves the connection itself: it does not say what it wraps.
type opaqueLayer struct{ loopLineH }

// liarLayer says it wraps another handler than the one it was given.
type liarLayer struct {
	corenet.LoopLayer
	other corenet.LoopHandler
}

func (l *liarLayer) Next() corenet.LoopHandler { return l.other }

// keepLayer forwards every callback but the one it is told to keep.
type keepLayer struct {
	corenet.LoopLayer
	keep int
}

func (l *keepLayer) OnStart(lanes int) {
	if l.keep != probeStart {
		l.LoopLayer.OnStart(lanes)
	}
}

func (l *keepLayer) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	if l.keep != probeOpen {
		l.LoopLayer.OnOpen(io, ref)
	}
}

func (l *keepLayer) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	if l.keep == probeData {
		return len(buf)
	}
	return l.LoopLayer.OnData(io, ref, buf)
}

func (l *keepLayer) OnTimeout(io corenet.LoopIO, ref corenet.LoopConnRef) {
	if l.keep != probeTimeout {
		l.LoopLayer.OnTimeout(io, ref)
	}
}

func (l *keepLayer) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	if l.keep != probeWritable {
		l.LoopLayer.OnWritable(io, ref)
	}
}

func (l *keepLayer) OnBatchEnd(io corenet.LoopIO) {
	if l.keep != probeBatchEnd {
		l.LoopLayer.OnBatchEnd(io)
	}
}

func (l *keepLayer) OnClose(io corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	if l.keep != probeClose {
		l.LoopLayer.OnClose(io, ref, err)
	}
}

func (l *keepLayer) Honours(k corenet.OptionKind) bool {
	if l.keep == probeHonours {
		return false
	}
	return l.LoopLayer.Honours(k)
}

// TestLoopChain_refusesABrokenLayer pins LoopMiddlewareInvalid: a layer that
// does not implement LoopLayered, one whose Next is not what it was given,
// and one that keeps any of the eight callbacks fail Start before anything is
// bound, naming the callback.
func TestLoopChain_refusesABrokenLayer(t *testing.T) {
	type tc struct {
		name, want string
		mw         corenet.Middleware[corenet.LoopHandler]
	}
	tests := []tc{
		{name: "not layered", want: "Next", mw: func(corenet.LoopHandler) corenet.LoopHandler { return &opaqueLayer{} }},
		{name: "another next", want: "Next", mw: func(next corenet.LoopHandler) corenet.LoopHandler {
			return &liarLayer{LoopLayer: corenet.NewLoopLayer(next), other: &loopLineH{}}
		}},
	}
	for cb := range probeCallbacks {
		tests = append(tests, tc{name: "keeps " + probeNames[cb], want: probeNames[cb], mw: func(next corenet.LoopHandler) corenet.LoopHandler {
			return &keepLayer{LoopLayer: corenet.NewLoopLayer(next), keep: cb}
		}})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable})).
				UseLoop(c.mw).HandleLoop(&loopLineH{})
			err := srv.Start(t.Context())
			if !errs.HasCode(err, corenet.CodeLoopMiddlewareInvalid) {
				t.Fatalf("Start = %v, want LoopMiddlewareInvalid", err)
			}
			if !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
				return f.Key() == "callback" && f.StringValue() == c.want
			}) {
				t.Fatalf("fields %v, want callback=%s", errs.FieldsOf(err), c.want)
			}
			if n := len(srv.State().Listeners); n != 0 {
				t.Fatalf("a refused Start bound %d listeners", n)
			}
		})
	}
}

// honourLayer forwards everything and honours one option itself.
type honourLayer struct {
	corenet.LoopLayer
	k corenet.OptionKind
}

func (l *honourLayer) Honours(k corenet.OptionKind) bool { return k == l.k || l.LoopLayer.Honours(k) }

// TestLoopChain_mergesHonours pins that the chain honours what one of its
// layers or its handler honours: ReadTimeout on a loop group is refused
// unless a layer applies it, and the handler's own options still count
// behind a layer that honours nothing.
func TestLoopChain_mergesHonours(t *testing.T) {
	plain := func(next corenet.LoopHandler) corenet.LoopHandler {
		return &traceLayer{LoopLayer: corenet.NewLoopLayer(next), trace: &chainTrace{}}
	}
	timeout := func(next corenet.LoopHandler) corenet.LoopHandler {
		return &honourLayer{LoopLayer: corenet.NewLoopLayer(next), k: corenet.OptionReadTimeout}
	}
	tests := []struct {
		name string
		h    corenet.LoopHandler
		mws  []corenet.Middleware[corenet.LoopHandler]
		ok   bool
	}{
		{name: "nobody honours it", h: &loopLineH{}, mws: []corenet.Middleware[corenet.LoopHandler]{plain}},
		{name: "a layer honours it", h: &loopLineH{}, mws: []corenet.Middleware[corenet.LoopHandler]{plain, timeout}, ok: true},
		{
			name: "the handler honours it behind a layer", h: &honouringH{loopLineH{honours: []corenet.OptionKind{corenet.OptionReadTimeout}}},
			mws: []corenet.Middleware[corenet.LoopHandler]{plain}, ok: true,
		},
		{name: "UseLoop with no middleware", h: &loopLineH{}, ok: false},
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv := New()
			t.Cleanup(func() { swallowErr(srv.Close()) })
			srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(corenet.LoopValue{Backend: corenet.LoopBackendPortable}),
				ReadTimeout(time.Second)).UseLoop(c.mws...).HandleLoop(c.h)
			err := srv.Start(t.Context())
			if c.ok != (err == nil) {
				t.Fatalf("Start = %v, want served %v", err, c.ok)
			}
			if !c.ok && !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) {
				t.Fatalf("Start = %v, want LoopOptionUnsupported", err)
			}
		})
	}
}

// TestUseLoop_refusedWithoutALoopHandler pins that a group no loop handler
// serves refuses UseLoop, even with no middleware, naming it (R6).
func TestUseLoop_refusedWithoutALoopHandler(t *testing.T) {
	t.Parallel()
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("conn", Listen("tcp", "127.0.0.1:0")).UseLoop().HandleFunc(func(_ context.Context, c corenet.Conn) error { return c.Close() })
	err := srv.Start(t.Context())
	if !errs.HasCode(err, corenet.CodeLoopOptionUnsupported) || !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
		return f.Key() == "option" && f.StringValue() == "UseLoop"
	}) {
		t.Fatalf("Start = %v, want LoopOptionUnsupported option=UseLoop", err)
	}
}

// TestStreamGroup_UseLoop_recordsItsKind pins that UseLoop records
// OptionLoopMiddleware, even with no middleware.
func TestStreamGroup_UseLoop_recordsItsKind(t *testing.T) {
	t.Parallel()
	g := (&StreamGroup{}).UseLoop()
	if got := g.kinds.list(); !slices.Equal(got, []corenet.OptionKind{corenet.OptionLoopMiddleware}) {
		t.Fatalf("UseLoop() recorded %v, want [UseLoop]", got)
	}
}

// Test_sameHandler pins the identity check: a different value or type is
// another handler, and a value of a type that cannot be compared is taken as
// the same rather than compared into a panic.
func Test_sameHandler(t *testing.T) {
	t.Parallel()
	a, b := &loopLineH{}, &loopLineH{}
	switch {
	case !sameHandler(a, a), sameHandler(a, b), sameHandler(a, nil), !sameHandler(nil, nil):
		t.Fatal("pointer identity")
	case !sameHandler(uncomparableH{}, uncomparableH{}):
		t.Fatal("an uncomparable value")
	}
}

// uncomparableH is a loop handler held by value, of a type == cannot compare.
type uncomparableH struct{ tags []int }

func (uncomparableH) OnOpen(corenet.LoopIO, corenet.LoopConnRef)                   {}
func (uncomparableH) OnData(_ corenet.LoopIO, _ corenet.LoopConnRef, b []byte) int { return len(b) }
func (uncomparableH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)                {}
func (uncomparableH) OnWritable(corenet.LoopIO, corenet.LoopConnRef)               {}
func (uncomparableH) OnBatchEnd(corenet.LoopIO)                                    {}
func (uncomparableH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error)           {}
