//go:build !race

package sse_test

import (
	"testing"
)

// perfLoopStreamValueSend is LoopStream.Send's fixture: one small event, framed and
// handed to a loop that counts it.
func perfLoopStreamValueSend(tb testing.TB) func() {
	src := &source{}
	_, f := open(tb, src, sseReq)
	st := src.opened[0]
	ev := benchEvent
	return func() {
		if err := st.Send(ev); err != nil {
			tb.Fatal(err)
		}
		f.out[ref1] = f.out[ref1][:0]
	}
}
