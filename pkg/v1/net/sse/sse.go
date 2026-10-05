//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/sse .

// Package sse is the server side of Server-Sent Events: a unidirectional
// server→client stream over an ordinary HTTP response.
//
// # The whole thing
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		stream, err := sse.New(w, r)
//		if err != nil {
//			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
//			return
//		}
//		defer stream.Close()
//
//		for {
//			select {
//			case <-stream.Done():
//				return
//			case ev := <-events:
//				if err := stream.Send(sse.Event{ID: ev.Cursor, Name: "tick", Data: ev.JSON}); err != nil {
//					return
//				}
//			}
//		}
//	}
//
// It is written against net/http's own interfaces, so it works in any
// http.Handler. Mounted on this SDK's engine — Group.HandleHTTP — it
// additionally observes the server's drain signal, which is what stops an
// endless stream from holding a graceful shutdown open for its whole budget.
//
// # Why a newline is not an error
//
// The format has no escape mechanism. A line terminator inside a value is not
// quoted, it SPLITS the value across several "data:" lines, which the client
// rejoins with "\n" — so a multi-line payload is expressible and exact. The
// same absence of escaping makes a terminator inside [Event].ID or
// [Event].Name unrepresentable, and those are refused rather than truncated: a
// silently shortened id is a resume token pointing at the wrong place.
//
// # Reconnection, and what the SDK does not do
//
// A client stores the last non-empty id it saw and sends it back in the
// Last-Event-ID header when it reconnects. [Stream].LastEventID hands that
// cursor to the handler. Nothing is replayed from it, on purpose.
//
// A replay buffer held by the stream would be empty at exactly the moment a
// resume needs it — a reconnect is a NEW connection and therefore a new stream.
// A buffer that outlived the stream would be an application store: it has to
// know how many events to keep, how long they stay valid, and whether replaying
// them is even safe, which are questions about what the events MEAN and not
// about the transport. So the cursor is handed over and the handler resumes
// from its own log. Minting ids is the handler's job for the same reason: an id
// the SDK invented would be a number that means nothing to the application the
// client sends it back to.
//
// # Keep-alive
//
// An idle stream is indistinguishable from a dead one to a proxy counting idle
// seconds. [Stream] therefore sends a periodic comment — ignored by every
// client, so it can never be mistaken for an event. The interval defaults to
// [DefaultKeepAlive]; a zero passed to [KeepAlive] is clamped to it rather than
// silently meaning "never", and "never" is spelled [WithoutKeepAlive].
//
// # Shutdown
//
// [Stream].Done closes when the client disconnects, when the handler calls
// [Stream].Close, or when the server begins draining. [Stream].Send refuses
// once it has, so a handler that only ever calls Send in a loop terminates too.
// Both halves matter: without the second, one handler shape could still hold a
// graceful shutdown open until its budget expired.
//
// # Clients
//
// This package is the server half only. The SDK's outbound client returns a
// fully-read body by design and cannot consume a stream that never ends; see
// the package CLAUDE.md for why that is a separate decision rather than an
// omission.
package sse
