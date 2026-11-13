package multi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/multi"
)

// echo is a BatchingStateMachine that answers each command with itself and
// records the order it applied them in.
type echo struct {
	mu      sync.Mutex
	applied []string
	batches int
}

func (m *echo) Apply(e coreconsensus.EntryValue) any {
	r := make([]any, 1)
	m.ApplyBatch([]coreconsensus.EntryValue{e}, r)
	return r[0]
}

func (m *echo) ApplyBatch(es []coreconsensus.EntryValue, results []any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches++
	for i, e := range es {
		m.applied = append(m.applied, string(e.Data))
		results[i] = "re:" + string(e.Data)
	}
}

func (*echo) Snapshot() (coreconsensus.Snapshot, error)                { return nil, nil }
func (*echo) Restore(io.Reader, coreconsensus.SnapshotMetaValue) error { return nil }

// fakeNode is a Replicated whose Propose applies the entry through its machine
// once the test releases it, and counts the entries.
type fakeNode struct {
	m       *multi.Machine
	gate    chan struct{}
	entered chan struct{}
	mu      sync.Mutex
	index   uint64
	result  func(any) any
	err     error
}

func newFake(inner coreconsensus.BatchingStateMachine) *fakeNode {
	return &fakeNode{m: multi.NewMachine(inner), entered: make(chan struct{}, 1024)}
}

func (f *fakeNode) Propose(ctx context.Context, data []byte) (coreconsensus.TokenValue, any, error) {
	f.entered <- struct{}{}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return coreconsensus.TokenValue{}, nil, errs.Wrap(ctx.Err(), coreconsensus.OutcomeUnknown.Params())
		}
	}
	if f.err != nil {
		return coreconsensus.TokenValue{}, nil, f.err
	}
	// One apply at a time, as the engine's apply goroutine.
	f.mu.Lock()
	defer f.mu.Unlock()
	f.index++
	tok := coreconsensus.TokenValue{Term: 1, Index: f.index}
	val := f.m.Apply(coreconsensus.EntryValue{Position: tok.Position(), Kind: coreconsensus.EntryCommand, Data: data, Token: tok})
	if f.result != nil {
		val = f.result(val)
	}
	return tok, val, nil
}

func (*fakeNode) Barrier(context.Context) (coreconsensus.TokenValue, error) {
	return coreconsensus.TokenValue{}, nil
}
func (*fakeNode) WaitApplied(context.Context, coreconsensus.TokenValue) error { return nil }
func (*fakeNode) Status() coreconsensus.StatusValue                           { return coreconsensus.StatusValue{} }

func closePacker(t *testing.T, p *multi.Packer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestCommandsWaitingTogetherLeaveInOneEntryAndEachGetsItsOwnResult(t *testing.T) {
	inner := &echo{}
	f := newFake(inner)
	f.gate = make(chan struct{})
	p := multi.NewPacker(f, multi.Config{MaxInFlight: 1})
	defer closePacker(t, p)

	// The first command leaves alone and holds the entry in flight while
	// the others queue behind it.
	type got struct {
		res multi.ResultValue
		err error
	}
	first := make(chan got, 1)
	go func() {
		r, err := p.Propose(context.Background(), []byte("first"))
		first <- got{r, err}
	}()
	<-f.entered
	const n = 50
	out := make([]chan got, n)
	for i := range n {
		out[i] = make(chan got, 1)
		go func() {
			r, err := p.Propose(context.Background(), fmt.Appendf(nil, "c%d", i))
			out[i] <- got{r, err}
		}()
	}
	waitQueued(t, p, n)
	close(f.gate)
	if g := <-first; g.err != nil || g.res.Value != "re:first" || g.res.Sub != 0 || g.res.Token.Index != 1 {
		t.Fatalf("first: %+v %v", g.res, g.err)
	}
	subs := map[int]bool{}
	for i := range n {
		g := <-out[i]
		if g.err != nil {
			t.Fatal(g.err)
		}
		if g.res.Value != fmt.Sprintf("re:c%d", i) || g.res.Token.Index != 2 {
			t.Fatalf("caller %d: %+v", i, g.res)
		}
		if inner.applied[1+g.res.Sub] != fmt.Sprintf("c%d", i) {
			t.Fatalf("caller %d at sub %d, applied %q", i, g.res.Sub, inner.applied[1+g.res.Sub])
		}
		subs[g.res.Sub] = true
	}
	if len(subs) != n || inner.batches != 2 {
		t.Fatalf("%d sub-indexes, %d batches", len(subs), inner.batches)
	}
}

// waitQueued waits until n commands are queued behind the entry in flight.
func waitQueued(t *testing.T, p *multi.Packer, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for multi.Queued(p) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d queued, want %d", multi.Queued(p), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAnEntryIsCutAtItsBounds(t *testing.T) {
	inner := &echo{}
	f := newFake(inner)
	f.gate = make(chan struct{})
	p := multi.NewPacker(f, multi.Config{MaxCommands: 3, MaxInFlight: 1})
	defer closePacker(t, p)
	go func() { _, _ = p.Propose(context.Background(), []byte("hold")) }()
	<-f.entered
	var wg sync.WaitGroup
	for i := range 7 {
		wg.Go(func() {
			if _, err := p.Propose(context.Background(), fmt.Appendf(nil, "%d", i)); err != nil {
				t.Error(err)
			}
		})
	}
	waitQueued(t, p, 7)
	close(f.gate)
	wg.Wait()
	// One entry in flight, then 3, 3 and 1.
	if inner.batches != 4 {
		t.Fatalf("%d entries, want 4", inner.batches)
	}

	_, err := multi.NewPacker(f, multi.Config{MaxBytes: 64}).Propose(context.Background(), make([]byte, 64))
	if !errs.HasCode(err, coreconsensus.CodeEntryTooLarge) {
		t.Fatalf("err %v, want ENTRY_TOO_LARGE", err)
	}
}

func TestACancelledCallerIsAnsweredByWhetherItsCommandLeft(t *testing.T) {
	inner := &echo{}
	f := newFake(inner)
	f.gate = make(chan struct{})
	p := multi.NewPacker(f, multi.Config{MaxInFlight: 1})
	defer closePacker(t, p)

	// In flight: the outcome is unknown.
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := p.Propose(ctx, []byte("inflight"))
		errc <- err
	}()
	<-f.entered
	// Queued: withdrawn, never proposed.
	qctx, qcancel := context.WithCancel(context.Background())
	qerr := make(chan error, 1)
	go func() {
		_, err := p.Propose(qctx, []byte("queued"))
		qerr <- err
	}()
	waitQueued(t, p, 1)
	qcancel()
	if err := <-qerr; !errors.Is(err, context.Canceled) || errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("queued: %v", err)
	}
	cancel()
	if err := <-errc; !errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("in flight: %v", err)
	}
	close(f.gate)
	closePacker(t, p)
	for _, c := range inner.applied {
		if c == "queued" {
			t.Fatal("a withdrawn command was proposed")
		}
	}
}

func TestTheEntrysAnswerReachesEveryCallerPackedInIt(t *testing.T) {
	type tc struct {
		name   string
		err    error
		result func(any) any
		code   errs.Code
		token  bool
	}
	cases := []tc{
		{name: "propose fails", err: errs.Wrap(coreconsensus.NotLeader, errs.WrapParams{}), code: coreconsensus.CodeNotLeader},
		{name: "body refused", result: func(any) any { return errs.Wrap(coreconsensus.FrameInvalid, errs.WrapParams{}) }, code: coreconsensus.CodeFrameInvalid},
		{name: "not a Machine", result: func(any) any { return "whole" }, code: coreconsensus.CodeResultTypeMismatch, token: true},
		{name: "short", result: func(any) any { return []any{} }, code: coreconsensus.CodeResultTypeMismatch, token: true},
	}
	runCase := func(t *testing.T, c tc) {
		f := newFake(&echo{})
		f.err, f.result = c.err, c.result
		p := multi.NewPacker(f, multi.Config{MaxInFlight: 1})
		defer closePacker(t, p)
		res, err := p.Propose(context.Background(), []byte("x"))
		if !errs.HasCode(err, c.code) {
			t.Fatalf("err %v, want %v", err, c.code)
		}
		if c.token != (res.Token.Index != 0) {
			t.Fatalf("token %+v", res.Token)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}

func TestAClosedPackerAnswersNodeStopped(t *testing.T) {
	f := newFake(&echo{})
	f.gate = make(chan struct{})
	p := multi.NewPacker(f, multi.Config{MaxInFlight: 1})
	go func() { _, _ = p.Propose(context.Background(), []byte("hold")) }()
	<-f.entered
	errc := make(chan error, 1)
	go func() {
		_, err := p.Propose(context.Background(), []byte("queued"))
		errc <- err
	}()
	waitQueued(t, p, 1)
	// The entry in flight outlives Close's ctx: it is cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Close(ctx); !errs.HasCode(err, coreconsensus.CodeOutcomeUnknown) {
		t.Fatalf("close: %v", err)
	}
	if err := <-errc; !errs.HasCode(err, coreconsensus.CodeNodeStopped) {
		t.Fatalf("queued: %v", err)
	}
	if _, err := p.Propose(context.Background(), []byte("late")); !errs.HasCode(err, coreconsensus.CodeNodeStopped) {
		t.Fatalf("late: %v", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestAMultiMachineAppliesABatchOfEntriesInOneInnerCall(t *testing.T) {
	inner := &echo{}
	m := multi.NewMachine(inner)
	es := []coreconsensus.EntryValue{
		{Data: multi.Body("a", "b")},
		{Data: []byte("not packed")},
		{Data: multi.Body("c")},
	}
	results := make([]any, len(es))
	m.ApplyBatch(es, results)
	if inner.batches != 1 || fmt.Sprint(inner.applied) != "[a b c]" {
		t.Fatalf("%d calls, applied %q", inner.batches, inner.applied)
	}
	if fmt.Sprint(results[0]) != "[re:a re:b]" || fmt.Sprint(results[2]) != "[re:c]" {
		t.Fatalf("results %v", results)
	}
	if err, _ := results[1].(error); !errs.HasCode(err, coreconsensus.CodeFrameInvalid) {
		t.Fatalf("unpacked entry: %v", results[1])
	}
}

func TestUpToMaxInFlightEntriesLeaveWithoutWaiting(t *testing.T) {
	inner := &echo{}
	f := newFake(inner)
	f.gate = make(chan struct{})
	p := multi.NewPacker(f, multi.Config{MaxInFlight: 3})
	defer closePacker(t, p)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if _, err := p.Propose(context.Background(), fmt.Appendf(nil, "%d", i)); err != nil {
				t.Error(err)
			}
		})
		if i < 3 {
			<-f.entered
		}
	}
	// Three entries are in flight; the fourth command waits for a slot.
	waitQueued(t, p, 1)
	close(f.gate)
	wg.Wait()
	if inner.batches != 4 {
		t.Fatalf("%d entries, want 4", inner.batches)
	}
}
