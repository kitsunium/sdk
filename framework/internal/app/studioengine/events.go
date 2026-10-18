package studioengine

import (
	"encoding/json"
	"net/http"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/net/sse"
)

// serveEvents streams the app's live events as Server-Sent Events. The
// stream opens with a hello carrying the graph revision, so a Studio that
// reconnects after a reload knows whether to fetch the graph again.
//
// The stream itself — framing, the keep-alive comment a proxy needs to see,
// the bound on each frame's write, the end on disconnect — is the SDK's
// net/sse; the Studio's own is only which events, in which order.
func serveEvents(host plug.StudioHost) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		stream, err := sse.New(w, r)
		if err != nil {
			host.Fail(w, "internal error")
			return
		}
		defer func() {
			if err := stream.Close(); err != nil {
				host.Debug(r.Context(), "the Studio's event stream did not close cleanly", err)
			}
		}()
		relay(host, stream)
	}
}

// relay sends the hello, then every event the host streams, until the
// client, the stream or the app stops. An event that does not encode is
// skipped; one that cannot be written ends the stream.
func relay(host plug.StudioHost, stream *sse.Stream) {
	events, cancel := host.Subscribe()
	defer cancel()
	send := func(e model.Event) error {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil
		}
		return stream.Send(sse.Event{Data: string(raw)})
	}
	g := host.Graph()
	if send(model.Event{Type: model.EventHello, Time: host.Now().UTC(), Revision: g.Revision, StartedAt: g.App.StartedAt, Phase: host.Phase()}) != nil {
		return
	}
	for {
		select {
		case <-stream.Done():
			return
		case <-host.Stopping():
			return
		case e := <-events:
			if send(e) != nil {
				return
			}
		}
	}
}
