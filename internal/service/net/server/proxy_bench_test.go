package server

import (
	"bytes"
	stdnet "net"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
)

// BenchmarkProxyHeader measures a v2 header parsed off a stream, one per
// connection: it allocates nothing, the announced address held in the conn.
func BenchmarkProxyHeader(b *testing.B) {
	header := v2Header(stdnet.ParseIP("203.0.113.7"), 4242)
	scratch := buffer.GetSize(proxyBufferBytes)
	defer buffer.Put(scratch)
	buf := (*scratch)[:proxyBufferBytes]
	r := bytes.NewReader(nil)
	var out announcedClient
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(header)
		if err := readProxyHeader(r, buf, &out); err != nil {
			b.Fatal(err)
		}
	}
}
