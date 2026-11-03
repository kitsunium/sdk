package nettransport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"time"

	corenettransport "github.com/kitsunium/sdk/internal/core/observe/logger/writer/nettransport"
	"github.com/kitsunium/sdk/internal/service/observe/logger/internal/datagram"
)

// defaultHTTPTimeout bounds an HTTP POST so a stalled collector cannot wedge the
// drainer goroutine indefinitely when the caller supplies no HTTPClient.
const defaultHTTPTimeout time.Duration = 30 * time.Second

// httpDrainMaxBytes bounds how much of a collector's response body is drained
// after the verdict — see postRecord.
//
// There is no bounded read here for it to share a figure with: postRecord judges
// a delivery by its status alone and reads nothing of the body, so the drain is
// the only read of that remote-controlled length. It is the 1 MiB the OTLP
// exporters bound their response with (DefaultOTLPMaxResponseBytes in
// service/observe/metrics and service/observe/trace), so the SDK's three HTTP emitters read a
// collector's body the same way — and it sits above the 256 KiB net/http drains
// by itself after an early Close, which it does not even attempt for a
// response declaring more.
const httpDrainMaxBytes int64 = 1 << 20

// httpFallbackIdleTimeout is how long the default client's fallback transport
// keeps an idle connection — see newHTTPTransport. It is net/http's own
// DefaultTransport value, so the fallback reaps its pool exactly as the clone it
// stands in for does, rather than holding a connection for as long as the
// collector tolerates it.
const httpFallbackIdleTimeout time.Duration = 90 * time.Second

// contentTypeJSON labels the POST body; the encoder owns the actual format, but
// JSON is the de-facto log-ingestion content type (Loki/Datadog/Elastic).
const contentTypeJSON string = "application/json"

// contentTypeNDJSON labels an HTTPBatchNDJSON body: newline-delimited JSON,
// one record per line (#807).
const contentTypeNDJSON string = "application/x-ndjson"

// httpStatusFloor / httpStatusCeil bound the accepted 2xx success range.
const (
	httpStatusFloor int = 200
	httpStatusCeil  int = 300
)

// newConnSeam dials network/addr (via dialer, or net.Dial when nil) and returns
// a sendFunc that writes each record verbatim to the connection, the batch seam
// and the connection's Close as the closer. The encoder owns line framing; for
// udp the datagram boundary is the frame, for tcp the encoder's trailing newline
// delimits records — so send writes p as-is and stays 0 alloc. sendBatch (#807)
// writes a whole backlog with one net.Buffers.WriteTo — one writev(2) on a
// *net.TCPConn, sequential Writes on a connection that does not take a vector —
// and reports how many records landed whole. A udp datagram is a record and
// gathering several would merge them, so udp's sendBatch is a
// datagram.BatchSender instead: one sendmmsg(2) per backlog on Linux, each
// record still its own datagram, one Write each elsewhere.
func newConnSeam(network, addr string, dialer func(network, addr string) (net.Conn, error)) (send sendFunc, sendBatch batchSendFunc, closer func() error, err error) {
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
		return nil, nil, nil, wrapDial(derr, network)
	}
	//: send writes the record verbatim; the caller's netSink holds the mutex.
	send = func(_ context.Context, p []byte) error {
		//: a single Write keeps a udp record in one datagram and stays 0 alloc.
		_, werr := conn.Write(p)
		//: return the raw cause; netSink.Write wraps it with the write sentinel.
		return werr
	}
	//: a datagram socket keeps one datagram per record, sent in one call.
	if network != protoTCP {
		//: the scratch is the sender's, allocated once; the netSink's mutex
		//: serialises its use.
		return send, datagramBatch(datagram.NewBatchSender(conn)), conn.Close, nil
	}
	//: the vector WriteTo consumes; the caller's netSink holds the mutex, so
	//: one scratch serves every batch and the caller's slice is never touched.
	var vec net.Buffers
	sendBatch = func(_ context.Context, payloads [][]byte) (written int, err error) {
		vec = append(vec[:0], payloads...)
		n, werr := vec.WriteTo(conn)
		//: drop the references the consumed vector still holds.
		clear(vec[:cap(vec)])
		//: the records that landed whole, and the raw cause.
		return wholeRecords(payloads, n), werr
	}
	//: hand back the send + the connection's Close as the closer.
	return send, sendBatch, conn.Close, nil
}

// datagramBatch adapts a datagram.BatchSender to the batch seam.
func datagramBatch(sender *datagram.BatchSender) batchSendFunc {
	//: the context bounds nothing a datagram send waits on.
	return func(_ context.Context, payloads [][]byte) (written int, err error) {
		//: one datagram per record, in as few calls as the kernel allows.
		return sender.Send(payloads)
	}
}

// wholeRecords counts the payloads, from the first, that lie entirely within
// the first n bytes of their concatenation.
func wholeRecords(payloads [][]byte, n int64) int {
	//: walk the payloads until one does not fit in what was written.
	for i, p := range payloads {
		//: this record was cut, or never started.
		if int64(len(p)) > n {
			return i
		}
		n -= int64(len(p))
	}
	//: all of them.
	return len(payloads)
}

// newHTTPSeams returns a sendFunc that POSTs each record to url (via client, or a
// timeout-bounded default when nil). Unlike the conn seam this allocates per
// send (request + reader) — HTTP egress is not a 0-alloc path; the 0-alloc
// invariant is the conn seam's.
//
// The closer releases the idle connections of the pool the sink OWNS, which is
// the default client's and nothing else. A caller-supplied HTTPClient is used as
// given and its pool is left to the caller: calling CloseIdleConnections on it
// would reach whatever else shares that pool — for a client with a nil
// Transport, every other client in the process — and emptying a shared pool is
// the very call newHTTPTransport explains is not harmless.
//
// When ndjson is true it also returns the batch call
// HTTPBatchNDJSON opts into (#807): the backlog the drainer holds posted as
// ONE application/x-ndjson body, a record per line — a record that does not
// end its line gains the newline —, through the same client and verdict. A
// refused POST delivered none of the batch. Without ndjson sendBatch is nil
// and the drainer hands records one at a time, the wire unchanged. The body
// is built fresh per batch: the transport may still be reading it after the
// verdict, so it is never reused.
func newHTTPSeams(url string, client *http.Client, ndjson bool) (send sendFunc, sendBatch batchSendFunc, closer func() error) {
	//: a supplied client is the caller's, used as given.
	c := client
	//: its pool is the caller's to release, so by default there is none to close.
	closer = func() error {
		//: nothing of the caller's is closed here.
		return nil
	}
	//: the fallback is a one-time construction branch, not a hot path.
	if c == nil {
		//: bounded, redirect-refusing, and on a pool of its own — see
		//: newHTTPClient.
		c = newHTTPClient(defaultHTTPTimeout)
		//: the sink owns that pool, so closing the sink releases it.
		closer = func() error {
			//: its own pool: nothing else in the process rides it.
			c.CloseIdleConnections()
			//: nothing can fail here.
			return nil
		}
	}
	//: send POSTs the record body and verifies a 2xx response.
	send = func(ctx context.Context, p []byte) error {
		//: post the record and map a non-2xx status to the write sentinel.
		return postRecord(ctx, c, url, p)
	}
	//: the batch call only when the caller opted into the NDJSON wire.
	if ndjson {
		sendBatch = func(ctx context.Context, payloads [][]byte) (written int, err error) {
			//: one POST for the batch, or none of it delivered.
			if perr := postBody(ctx, c, url, ndjsonBody(payloads), contentTypeNDJSON); perr != nil {
				return 0, perr
			}
			//: every record, in one body.
			return len(payloads), nil
		}
	}
	//: hand back the send, the batch call and the idle-teardown closer.
	return send, sendBatch, closer
}

// ndjsonBody joins payloads into one NDJSON document, each record on its
// own line.
func ndjsonBody(payloads [][]byte) []byte {
	size := 0
	//: room for every record and a newline each.
	for _, p := range payloads {
		size += len(p) + 1
	}
	body := make([]byte, 0, size)
	//: in order, each record ending its line.
	for _, p := range payloads {
		body = append(body, p...)
		//: a record the encoder did not end gains its newline.
		if len(p) == 0 || p[len(p)-1] != '\n' {
			body = append(body, '\n')
		}
	}
	//: the document.
	return body
}

// newHTTPClient builds the default client: bounded by timeout, refusing to
// follow a redirect, and riding a connection pool of its own.
//
// The bound keeps a stalled collector from wedging the async drainer goroutine
// forever, and keeps zero-config usage safe. The redirect refusal is CWE-918: a
// consumer-controlled URL must not 30x-bounce the POST to an internal host past
// an allowlist that only validated the configured target, so CheckRedirect halts
// every redirect with the stdlib ErrUseLastResponse signal (not an error
// verdict) and the non-2xx last response surfaces downstream as a write
// failure. A caller-supplied HTTPClient owns its own redirect policy and is used
// as-is.
//
// The pool is newHTTPTransport's, which says why it is not the process's.
func newHTTPClient(timeout time.Duration) *http.Client {
	//: Timeout covers dial, write, read and body — the whole round trip.
	return &http.Client{
		Timeout: timeout,
		//: this sink's own pool, which nothing else in the process can empty.
		Transport: newHTTPTransport(),
		//: stop at the first redirect and return that response.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			//: stop following and hand back the redirect response as-is.
			return http.ErrUseLastResponse
		},
	}
}

// newHTTPTransport returns the connection pool the default client owns: a clone
// of http.DefaultTransport, so every setting a process expects of an outbound
// client — the proxy environment first of all — carries over, and none of its
// connections do.
//
// # Why the default client does not share http.DefaultTransport
//
// Because anything in the process can empty that pool, and net/http turns doing
// so at the wrong instant into a wrong verdict. A response with no body — a 204
// is how Loki answers a push — is put back in the idle pool BEFORE the waiting
// round trip is handed it, and a CloseIdleConnections landing in that window
// closes the connection under it: the round trip then reports "connection
// broken" for a response that had already arrived. Here that turns a delivered
// record into a failed write, which a failover or a retry then sends again — a
// duplicated log line. Every httptest.Server.Close and every
// http.DefaultClient.CloseIdleConnections is such a call, made by code this
// writer cannot see; so was this package's own closer, which used to empty
// that pool whenever an http sink closed.
//
// A caller-supplied HTTPClient is used as given, Transport included, so one that
// rides http.DefaultTransport inherits that exposure; giving it a Transport of
// its own is how it avoids it.
//
// The clone is a snapshot taken at construction. A process that has replaced
// http.DefaultTransport with something other than an *http.Transport leaves
// nothing to clone, and a fresh transport that still honours the proxy
// environment stands in; one that routes traffic through a custom RoundTripper
// passes it as the HTTPClient.
func newHTTPTransport() *http.Transport {
	//: the process default, while it is still the stdlib's type.
	if base, isTransport := http.DefaultTransport.(*http.Transport); isTransport {
		//: every setting, the Proxy function included, and none of the pool.
		return base.Clone()
	}
	//: replaced by a foreign RoundTripper: keep the proxy environment and the
	//: idle reaping the clone would have had.
	return &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: httpFallbackIdleTimeout}
}

// postRecord POSTs p to url and returns the raw cause on a transport error or
// the shared NetTransportWriteFailed sentinel on a non-2xx status. netSink.Write
// wraps the result (origin-wins keeps the sentinel's code on the status path).
//
// The body is drained after the verdict so the connection returns to the pool,
// and the drain is BOUNDED by httpDrainMaxBytes: it is the only read of a
// length a remote party controls, and a collector streaming an endless body held
// the async drainer here forever whenever the client had no deadline — the
// default client's Timeout covers the body, a supplied one is used as-is and may
// carry none. Past the bound the body is closed unread; net/http recycles only a
// connection whose response it has seen end, and the drain it attempts itself
// after an early Close is bounded as well, so a body that runs past both costs
// the connection — correct, since reading on to save one handshake is exactly
// what the bound refuses. The bound is on bytes, not time: a collector that
// stops sending mid-body is bounded only by the client's deadline.
func postRecord(ctx context.Context, c *http.Client, url string, p []byte) error {
	//: one record, labelled JSON.
	return postBody(ctx, c, url, p, contentTypeJSON)
}

// postBody is postRecord for a body of any content type: an NDJSON batch
// under HTTPBatchNDJSON, one JSON record otherwise.
func postBody(ctx context.Context, c *http.Client, url string, p []byte, contentType string) error {
	//: build a context-bound POST so cancellation aborts a slow request.
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(p))
	//: a malformed url surfaces as a raw request-build error.
	if rerr != nil {
		//: return the raw cause for netSink to wrap.
		return rerr
	}
	//: label the body so the collector parses it correctly.
	req.Header.Set("Content-Type", contentType)
	resp, derr := c.Do(req)
	//: a transport failure surfaces raw.
	if derr != nil {
		//: return the raw cause for netSink to wrap.
		return derr
	}
	//: drain + close the body so the keep-alive connection is reusable; the
	//: teardown outcome is irrelevant to the delivery verdict computed below.
	defer func() {
		//: drain the unread body so the connection returns to the pool, and
		//: no more than httpDrainMaxBytes of it.
		_, drainErr := io.Copy(io.Discard, io.LimitReader(resp.Body, httpDrainMaxBytes))
		//: swallow both teardown errors — the verdict already stands.
		swallowErr(drainErr)
		swallowErr(resp.Body.Close())
	}()
	//: a non-2xx status is a delivery failure — return the typed sentinel.
	if resp.StatusCode < httpStatusFloor || resp.StatusCode >= httpStatusCeil {
		//: origin-wins: netSink's wrap keeps this sentinel's code.
		return corenettransport.NetTransportWriteFailed
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
