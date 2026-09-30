// Package studiokit — the Studio's live events, as a Server-Sent Events
// stream.
package studiokit

import (
	"net/http"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/server/sse"
)

// eventStream is a Server-Sent Events stream of the SDK's server/sse: its
// framing, the keep-alive a proxy needs to see, the bound on each frame's
// write, the end on disconnect.
type eventStream struct {
	s *sse.Stream
}

// openEventStream opens a stream on w.
//
// IFACE-PLUGIN: kit reaches the stream through plug.EventStream; this is its
// one implementation.
func openEventStream(w http.ResponseWriter, r *http.Request) (plug.EventStream, error) {
	s, err := sse.New(w, r)
	if err != nil {
		return nil, err
	}
	return eventStream{s: s}, nil
}

// Send writes one event's data.
func (e eventStream) Send(data string) error { return e.s.Send(sse.Event{Data: data}) }

// Done is closed when the client left.
func (e eventStream) Done() <-chan struct{} { return e.s.Done() }

// Close ends the stream.
func (e eventStream) Close() error { return e.s.Close() }
