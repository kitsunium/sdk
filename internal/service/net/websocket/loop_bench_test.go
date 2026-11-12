package websocket_test

import (
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/websocket"
)

// discardWS drops every message: what the receive path alone costs.
type discardWS struct{ n int }

func (*discardWS) OpenWS(*corenet.HTTP1RequestValue) int                       { return 0 }
func (*discardWS) WSOpened(websocket.LoopConnValue, string)                    {}
func (d *discardWS) WSMessage(websocket.LoopConnValue, corenet.WSMessageValue) { d.n++ }
func (*discardWS) WSClosed(websocket.LoopConnValue, error)                     {}

// countingIO is a LoopIO that only counts its writes.
type countingIO struct{ writes int }

func (c *countingIO) Write(corenet.LoopConnRef, []byte) error            { c.writes++; return nil }
func (*countingIO) Close(corenet.LoopConnRef) error                      { return nil }
func (*countingIO) SetDeadline(corenet.LoopConnRef, time.Duration) error { return nil }
func (*countingIO) Pressure(corenet.LoopConnRef) bool                    { return false }
func (*countingIO) Post(corenet.PostFunc, uintptr) error                 { return nil }
func (*countingIO) Loop() int                                            { return 0 }

const wsHandshake = "GET /ws HTTP/1.1\r\nHost: h\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"

// frame4KiB is one masked 4 KiB binary frame.
var frame4KiB = func() []byte {
	payload := make([]byte, 4096)
	return frame(true, 0, 0x2, true, payload)
}()

// benchRef is the connection every fixture opens.
var benchRef = corenet.LoopConnRef{Slot: 1, Gen: 1}

// openLoopWS drives a handshake through the http1 adapter on a counting LoopIO.
func openLoopWS(tb testing.TB) (*http1.LoopHandler, *countingIO) {
	tb.Helper()
	_, a, io := openLoopWSHandler(tb)
	return a, io
}

// openLoopWSHandler is openLoopWS, the WebSocket handler the adapter upgraded
// to returned beside it.
func openLoopWSHandler(tb testing.TB) (*websocket.LoopHandler, *http1.LoopHandler, *countingIO) {
	tb.Helper()
	h, err := websocket.NewLoopHandler(&discardWS{}, websocket.AllowAnyOrigin(), websocket.WithoutPing())
	if err != nil {
		tb.Fatal(err)
	}
	a := http1.HandleLoop(h, http1.Parser{})
	io := &countingIO{}
	a.OnOpen(io, benchRef)
	if n := a.OnData(io, benchRef, []byte(wsHandshake)); n != len(wsHandshake) {
		tb.Fatal(n)
	}
	a.OnBatchEnd(io)
	return h, a, io
}

// BenchmarkWSLoopReceive4KiB is one masked 4 KiB frame read on an open
// connection: the header parsed, the payload unmasked in place, the message
// handed to the program with no copy and no allocation.
func BenchmarkWSLoopReceive4KiB(b *testing.B) {
	a, io := openLoopWS(b)
	data := make([]byte, len(frame4KiB))
	b.ReportAllocs()
	for b.Loop() {
		copy(data, frame4KiB)
		if n := a.OnData(io, benchRef, data); n != len(data) {
			b.Fatal(n)
		}
	}
}
