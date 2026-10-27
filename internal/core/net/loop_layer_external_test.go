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

// TestHTTP1AuthValue pins the subject held by value: a copy, refused past
// HTTP1MaxSubject without a change, a shorter one leaving nothing of the
// longer before it (two values with one subject compare equal), cleared by
// Reset with the claims.
func TestHTTP1AuthValue(t *testing.T) {
	t.Parallel()
	var a corenet.HTTP1AuthValue
	if len(a.Subject()) != 0 || a.Claims() != 0 {
		t.Fatal("the zero value holds something")
	}
	src := []byte("alice")
	if !a.SetSubject(src) {
		t.Fatal("SetSubject refused a short subject")
	}
	a.SetClaims(9)
	src[0] = 'X'
	if string(a.Subject()) != "alice" || a.Claims() != 9 {
		t.Fatalf("Subject %q, Claims %d", a.Subject(), a.Claims())
	}
	b := a
	b.SetSubject([]byte("bob"))
	if string(a.Subject()) != "alice" {
		t.Fatal("a copy shares the subject")
	}
	var fresh corenet.HTTP1AuthValue
	fresh.SetSubject([]byte("bob"))
	fresh.SetClaims(9)
	if b != fresh {
		t.Fatalf("a shorter subject left the longer one's bytes: %+v", b)
	}
	long := make([]byte, corenet.HTTP1MaxSubject+1)
	if a.SetSubject(long) || string(a.Subject()) != "alice" {
		t.Fatal("a subject past the bound was taken or cut")
	}
	if !a.SetSubject(long[:corenet.HTTP1MaxSubject]) || len(a.Subject()) != corenet.HTTP1MaxSubject {
		t.Fatal("a subject at the bound was refused")
	}
	a.Reset()
	if len(a.Subject()) != 0 || a.Claims() != 0 {
		t.Fatal("Reset left something")
	}
}
