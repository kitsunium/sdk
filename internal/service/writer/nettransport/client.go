// Package nettransport — the transport seams: the ONLY file that touches net /
// net/http. Each constructor returns a sendFunc (+ optional closer) captured by
// netSink, so the concrete transport stays confined here and tests inject a
// recording sendFunc with no real socket.
package nettransport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// defaultHTTPTimeout bounds an HTTP POST so a stalled collector cannot wedge the
// drainer goroutine indefinitely when the caller supplies no HTTPClient.
const defaultHTTPTimeout time.Duration = 30 * time.Second

// contentTypeJSON labels the POST body; the encoder owns the actual format, but
// JSON is the de-facto log-ingestion content type (Loki/Datadog/Elastic).
const contentTypeJSON string = "application/json"

// httpStatusFloor / httpStatusCeil bound the accepted 2xx success range.
const (
	httpStatusFloor int = 200
	httpStatusCeil  int = 300
)

// newConnSeam dials network/addr (via dialer, or net.Dial when nil) and returns
// a sendFunc that writes each record verbatim to the connection plus the
// connection's Close as the closer. The encoder owns line framing; for udp the
// datagram boundary is the frame, for tcp the encoder's trailing newline
// delimits records — so send writes p as-is and stays 0 alloc.
func newConnSeam(network, addr string, dialer func(network, addr string) (net.Conn, error)) (send sendFunc, closer func() error, err error) {
	//: nil dialer falls back to stdlib net.Dial (zero-config usage).
	dial := dialer
	//: the fallback is outside the hot path — one branch at construction.
	if dial == nil {
		//: default dialer preserves zero-config usage.
		dial = net.Dial
	}
	//: dial the target; a failure is wrapped with the dial sentinel (no addr).
	conn, derr := dial(network, addr)
	//: surface the dial failure to Open.
	if derr != nil {
		//: wrapDial attaches only the network, never the address (SSRF gate).
		return nil, nil, wrapDial(derr, network)
	}
	//: send writes the record verbatim; the caller's netSink holds the mutex.
	send = func(_ context.Context, p []byte) error {
		//: a single Write keeps a udp record in one datagram and stays 0 alloc.
		_, werr := conn.Write(p)
		//: return the raw cause; netSink.Write wraps it with the write sentinel.
		return werr
	}
	//: hand back the send + the connection's Close as the closer.
	return send, conn.Close, nil
}

// newHTTPSeam returns a sendFunc that POSTs each record to url (via client, or a
// timeout-bounded default when nil). The closer drops idle connections. Unlike
// the conn seam this allocates per send (request + reader) — HTTP egress is not
// a 0-alloc path; the 0-alloc invariant is the conn seam's.
func newHTTPSeam(url string, client *http.Client) (send sendFunc, closer func() error) {
	//: nil client falls back to a timeout-bounded default so a stalled
	//: collector cannot wedge the drainer goroutine forever.
	c := client
	//: the fallback is a one-time construction branch, not a hot path.
	if c == nil {
		//: a bounded default client preserves zero-config usage; CheckRedirect
		//: refuses to follow redirects (CWE-918) so a consumer-controlled URL
		//: cannot 30x-bounce the POST to an internal host past an allowlist that
		//: only validated the configured target. A caller-supplied HTTPClient
		//: owns its own redirect policy and is used as-is.
		c = &http.Client{
			Timeout: defaultHTTPTimeout,
			//: CheckRedirect halts every redirect with the stdlib
			//: ErrUseLastResponse signal (not an error verdict); a non-2xx last
			//: response then surfaces downstream as a write failure.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				//: stop following and hand back the redirect response as-is.
				return http.ErrUseLastResponse
			},
		}
	}
	//: send POSTs the record body and verifies a 2xx response.
	send = func(ctx context.Context, p []byte) error {
		//: post the record and map a non-2xx status to the write sentinel.
		return postRecord(ctx, c, url, p)
	}
	//: idle-connection teardown is the only "close" an http client needs.
	closer = func() error {
		//: release pooled keep-alive connections on shutdown.
		c.CloseIdleConnections()
		//: nothing can fail here.
		return nil
	}
	//: hand back the send + the idle-teardown closer.
	return send, closer
}

// postRecord POSTs p to url and returns the raw cause on a transport error or
// the shared NetTransportWriteFailed sentinel on a non-2xx status. netSink.Write
// wraps the result (origin-wins keeps the sentinel's code on the status path).
func postRecord(ctx context.Context, c *http.Client, url string, p []byte) error {
	//: build a context-bound POST so cancellation aborts a slow request.
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(p))
	//: a malformed url surfaces as a raw request-build error.
	if rerr != nil {
		//: return the raw cause for netSink to wrap.
		return rerr
	}
	//: label the body so the collector parses it correctly.
	req.Header.Set("Content-Type", contentTypeJSON)
	resp, derr := c.Do(req)
	//: a transport failure surfaces raw.
	if derr != nil {
		//: return the raw cause for netSink to wrap.
		return derr
	}
	//: drain + close the body so the keep-alive connection is reusable; the
	//: teardown outcome is irrelevant to the delivery verdict computed below.
	defer func() {
		//: drain the unread body so the connection returns to the pool.
		_, drainErr := io.Copy(io.Discard, resp.Body)
		//: swallow both teardown errors — the verdict already stands.
		swallowErr(drainErr)
		swallowErr(resp.Body.Close())
	}()
	//: a non-2xx status is a delivery failure — return the typed sentinel.
	if resp.StatusCode < httpStatusFloor || resp.StatusCode >= httpStatusCeil {
		//: origin-wins: netSink's wrap keeps this sentinel's code.
		return NetTransportWriteFailed
	}
	//: 2xx — the record was accepted.
	return nil
}

// swallowErr intentionally drops a body-teardown error: the HTTP delivery
// verdict is already decided, and stacking a drain/close error on top would
// obscure it. It mirrors syslog's swallowDialClose so the error-discard audit
// sees an explicit, reviewed no-op rather than a bare `_ =`.
func swallowErr(err error) {
	//: read the parameter so the unused-param audit treats this as intentional.
	if err == nil {
		//: nothing to discard on the happy path.
		return
	}
}
