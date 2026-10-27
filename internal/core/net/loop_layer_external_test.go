package net_test

import (
	"slices"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// countH counts the callbacks it received, and honours one option.
type countH struct {
	got   []string
	lanes int
}

func (h *countH) OnOpen(corenet.LoopIO, corenet.LoopConnRef)    { h.got = append(h.got, "open") }
func (h *countH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef) { h.got = append(h.got, "timeout") }
func (h *countH) OnWritable(corenet.LoopIO, corenet.LoopConnRef) {
	h.got = append(h.got, "writable")
}
func (h *countH) OnBatchEnd(corenet.LoopIO) { h.got = append(h.got, "batch") }
func (h *countH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) {
	h.got = append(h.got, "close")
}

func (h *countH) OnData(_ corenet.LoopIO, _ corenet.LoopConnRef, buf []byte) int {
	h.got = append(h.got, "data")
	return len(buf) - 1
}

// starterH is a countH that keeps per-lane state and honours ReadTimeout.
type starterH struct{ countH }

func (h *starterH) OnStart(lanes int)               { h.lanes = lanes }
func (*starterH) Honours(k corenet.OptionKind) bool { return k == corenet.OptionReadTimeout }

// nopIO is a LoopIO that does nothing.
type nopIO struct{}

func (nopIO) Write(corenet.LoopConnRef, []byte) error              { return nil }
func (nopIO) Close(corenet.LoopConnRef) error                      { return nil }
func (nopIO) SetDeadline(corenet.LoopConnRef, time.Duration) error { return nil }
func (nopIO) Pressure(corenet.LoopConnRef) bool                    { return false }
func (nopIO) Post(corenet.PostFunc, uintptr) error                 { return nil }
func (nopIO) Loop() int                                            { return 0 }

// TestLoopLayer pins the base of a loop middleware's layer: every callback
// reaches the wrapped handler, OnData returns what it consumed, Next is it,
// Honours answers for it and OnStart reaches a LoopStarter — and neither
// reaches a handler without the capability.
func TestLoopLayer(t *testing.T) {
	t.Parallel()
	h := &starterH{}
	l := corenet.NewLoopLayer(h)
	var ref corenet.LoopConnRef
	l.OnStart(4)
	l.OnOpen(nopIO{}, ref)
	if n := l.OnData(nopIO{}, ref, []byte("abc")); n != 2 {
		t.Fatalf("OnData = %d, want what the handler consumed", n)
	}
	l.OnTimeout(nopIO{}, ref)
	l.OnWritable(nopIO{}, ref)
	l.OnBatchEnd(nopIO{})
	l.OnClose(nopIO{}, ref, nil)
	if want := []string{"open", "data", "timeout", "writable", "batch", "close"}; !slices.Equal(h.got, want) {
		t.Fatalf("forwarded %v, want %v", h.got, want)
	}
	if h.lanes != 4 || l.Next() != corenet.LoopHandler(h) {
		t.Fatalf("OnStart %d, Next %v", h.lanes, l.Next())
	}
	if !l.Honours(corenet.OptionReadTimeout) || l.Honours(corenet.OptionTLS) {
		t.Fatal("Honours does not answer for the handler")
	}
	plain := corenet.NewLoopLayer(&countH{})
	plain.OnStart(2)
	if plain.Honours(corenet.OptionReadTimeout) {
		t.Fatal("a handler without the capability honours nothing")
	}
}
