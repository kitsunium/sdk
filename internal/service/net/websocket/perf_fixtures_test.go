//go:build !race

package websocket_test

import (
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// perfLoopHandlerOnUpgradedData is OnUpgradedData's fixture: one masked 4 KiB
// frame read on a connection the adapter upgraded, handed to the handler as the
// adapter hands it the bytes after the 101.
func perfLoopHandlerOnUpgradedData(tb testing.TB) func() {
	h, _, io := openLoopWSHandler(tb)
	s := corenet.HTTP1StreamValue{IO: io, Ref: benchRef}
	data := make([]byte, len(frame4KiB))
	return func() {
		copy(data, frame4KiB)
		if n := h.OnUpgradedData(s, data); n != len(data) {
			tb.Fatal(n)
		}
	}
}
