package sse_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// benchEvent is the small frame every loop figure sends.
var benchEvent = corenet.SSEEventValue{ID: "42", Name: "update", Data: `{"id":42,"ok":true}`}

// BenchmarkSSELoopSend is one event of an open stream on a fake loop: the frame
// written into the lane's buffer and handed to the loop's copying Write.
func BenchmarkSSELoopSend(b *testing.B) {
	src := &source{}
	_, f := open(b, src, sseReq)
	st := src.opened[0]
	b.ReportAllocs()
	for b.Loop() {
		if err := st.Send(benchEvent); err != nil {
			b.Fatal(err)
		}
		f.out[ref1] = f.out[ref1][:0]
	}
}
