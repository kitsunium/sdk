// Package metrics_test — the OTLP/HTTP emitter against a real socket.
//
// Every response the collector stub returns below is one the OTLP
// specification's "OTLP/HTTP Response" section describes, and every assertion
// is on the verdict that section prescribes — full success, partial success,
// a retryable status, a permanent rejection.
package metrics_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// endlessBodyBudget is how long an export against a collector that never stops
// sending may take before the test stops waiting. It is deliberately generous:
// the bounded export reads two megabytes at most over loopback and returns in
// milliseconds, so the budget exists only to turn a regression into a failure
// rather than a hang, and a tight one would turn a slow CI host into a failure.
const endlessBodyBudget time.Duration = 30 * time.Second

// reuseExports is how many exports the keep-alive test makes against one
// collector; TestOTLPHTTPReusesTheConnectionAcrossExports says why it is three.
const reuseExports int32 = 3

// proxyChildEnv tells the re-executed test binary which of the default
// client's two transports to build. It is set only on that child, so
// TestOTLPHTTPProxyChild skips in every ordinary run.
const proxyChildEnv string = "KTN_OTLP_METRICS_PROXY_CHILD"

// unresolvableEndpoint is a collector address that never resolves (".invalid",
// RFC 2606), so an export to it can only succeed through something other than
// DNS: the proxy named in the proxy child's environment, or a supplied client's
// own RoundTripper.
const unresolvableEndpoint string = "http://collector.invalid:4318" + svcmetrics.OTLPMetricsPath

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

// capturedRequest is what the collector stub saw, so the request side can be
// asserted as precisely as the response side.
type capturedRequest struct {
	mu      sync.Mutex
	method  string
	path    string
	headers http.Header
	body    string
	seen    int
}

// record stores one request.
func (c *capturedRequest) record(r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		body = nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.method, c.path, c.headers, c.body = r.Method, r.URL.Path, r.Header.Clone(), string(body)
	c.seen++
}

// snapshot copies the captured state out from under the lock.
func (c *capturedRequest) snapshot() capturedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return capturedRequest{method: c.method, path: c.path, headers: c.headers, body: c.body, seen: c.seen}
}

// RoundTrip implements http.RoundTripper.
func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// collectorStub starts an httptest server answering with status and body, and
// returns the exporter pointed at its /v1/metrics endpoint plus the capture.
func collectorStub(t *testing.T, status int, body string, header http.Header) (coremetrics.Exporter, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.record(r)
		for key, values := range header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(status)
		writeStub(w, body)
	}))
	t.Cleanup(server.Close)
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	return exporter, captured
}

// TestOTLPHTTPPostsTheEncodedDocument pins the request side against
// "OTLP/HTTP Request" and "JSON Protobuf Encoding": a POST to the metrics path
// under Content-Type: application/json, whose body is exactly what the encoder
// produces — no wrapper, no terminator, no re-encoding.
func TestOTLPHTTPPostsTheEncodedDocument(t *testing.T) {
	t.Parallel()
	exporter, captured := collectorStub(t, http.StatusOK, "", nil)
	snap := gaugeSnapshot(nil, 2.5)
	if err := exporter.Export(snap); err != nil {
		t.Fatalf("Export: unexpected error: %v", err)
	}
	seen := captured.snapshot()
	if seen.method != http.MethodPost {
		t.Errorf("method = %q, want POST", seen.method)
	}
	if seen.path != "/v1/metrics" {
		t.Errorf("path = %q, want /v1/metrics", seen.path)
	}
	if got := seen.headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if want := mustEncode(t, snap); seen.body != want {
		t.Errorf("body mismatch\n got: %s\nwant: %s", seen.body, want)
	}
}

// TestOTLPHTTPFullSuccess pins "Full Success": HTTP 200 with the
// partial_success field unset — including the empty body a collector may
// legitimately send.
func TestOTLPHTTPFullSuccess(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"empty body":          "",
		"empty message":       `{}`,
		"unset partial":       `{"partialSuccess":{}}`,
		"explicitly accepted": `{"partialSuccess":{"rejectedDataPoints":"0"}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			exporter, _ := collectorStub(t, http.StatusOK, body, nil)
			if err := exporter.Export(gaugeSnapshot(nil, 1)); err != nil {
				t.Errorf("a fully accepted request must not report an error, got %v", err)
			}
		})
	}
}

// TestOTLPHTTPPartialSuccess pins "Partial Success": data was LOST although the
// status was 200, and "the client MUST NOT retry the request".
//
// The two spellings of the count are both required: the specification says
// 64-bit integers are encoded as decimal strings "and either numbers or strings
// are accepted when decoding", so a decoder that read only one of them would
// silently report every partial success as a full one against the other kind of
// collector.
func TestOTLPHTTPPartialSuccess(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"string count": `{"partialSuccess":{"rejectedDataPoints":"7","errorMessage":"cardinality"}}`,
		"number count": `{"partialSuccess":{"rejectedDataPoints":7,"errorMessage":"cardinality"}}`,
		//: a JSON string may escape any character, digits included; trimming
		//: the quotes instead of decoding read this one as a full success.
		"escaped string count": `{"partialSuccess":{"rejectedDataPoints":"\u0037","errorMessage":"cardinality"}}`,
		"unknown field": `{"partialSuccess":{"rejectedDataPoints":"7"},` +
			`"somethingTheSpecAddedLater":{"nested":true}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			exporter, _ := collectorStub(t, http.StatusOK, body, nil)
			err := exporter.Export(gaugeSnapshot(nil, 1))
			if !errors.Is(err, svcmetrics.OTLPPartialSuccess) {
				t.Fatalf("want OTLPPartialSuccess, got %v", err)
			}
			if svcmetrics.OTLPRetryable(err) {
				t.Errorf("a partial success MUST NOT be retried")
			}
			if strings.Contains(err.Error(), "cardinality") {
				t.Errorf("the collector's free-text message must not reach the rendered error: %v", err)
			}
		})
	}
}

// TestOTLPHTTPIgnoresAnUnparseable200Body pins the posture a receiver is
// required to take in the other direction — ignore what you do not recognise.
// The 200 already said the request was accepted; inventing a loss from a
// malformed response would report a failure that did not happen, every scrape.
func TestOTLPHTTPIgnoresAnUnparseable200Body(t *testing.T) {
	t.Parallel()
	exporter, _ := collectorStub(t, http.StatusOK, "<html>proxy says hi</html>", nil)
	if err := exporter.Export(gaugeSnapshot(nil, 1)); err != nil {
		t.Errorf("an accepted request with an opaque body is still accepted, got %v", err)
	}
}

// TestOTLPHTTPRetryableStatuses pins "Retryable Response Codes" exactly: those
// four and no others.
func TestOTLPHTTPRetryableStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			exporter, _ := collectorStub(t, status, "", nil)
			err := exporter.Export(gaugeSnapshot(nil, 1))
			if !errors.Is(err, svcmetrics.OTLPExportUnavailable) {
				t.Fatalf("want OTLPExportUnavailable, got %v", err)
			}
			if !svcmetrics.OTLPRetryable(err) {
				t.Errorf("status %d is listed as retryable", status)
			}
		})
	}
}

// TestOTLPHTTPNonRetryableStatuses pins the other half of the same sentence:
// "All other 4xx or 5xx response status codes MUST NOT be retried". 500 and 501
// are the ones that matter — a class-based rule would replay both.
func TestOTLPHTTPNonRetryableStatuses(t *testing.T) {
	t.Parallel()
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusRequestEntityTooLarge,
		http.StatusInternalServerError,
		http.StatusNotImplemented,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			exporter, _ := collectorStub(t, status, "", nil)
			err := exporter.Export(gaugeSnapshot(nil, 1))
			if !errors.Is(err, svcmetrics.OTLPExportRejected) {
				t.Fatalf("want OTLPExportRejected, got %v", err)
			}
			if svcmetrics.OTLPRetryable(err) {
				t.Errorf("status %d is outside the retryable set", status)
			}
			if !kerrs.HasCode(err, svcmetrics.CodeOTLPExportRejected) {
				t.Errorf("want code 0.3.45.8 on the trail, got %v", err)
			}
		})
	}
}

// TestOTLPHTTPSurfacesRetryAfter pins "OTLP/HTTP Throttling": the collector's
// own back-off hint reaches the caller, who is the one that retries.
func TestOTLPHTTPSurfacesRetryAfter(t *testing.T) {
	t.Parallel()
	header := http.Header{"Retry-After": []string{"30"}}
	exporter, _ := collectorStub(t, http.StatusTooManyRequests, "", header)
	err := exporter.Export(gaugeSnapshot(nil, 1))
	if !errors.Is(err, svcmetrics.OTLPExportUnavailable) {
		t.Fatalf("want OTLPExportUnavailable, got %v", err)
	}
	if !hasField(err, "retry_after_seconds", "30") {
		t.Errorf("the throttling hint must reach the caller: %v", err)
	}
}

// TestOTLPHTTPIgnoresAnHTTPDateRetryAfter pins the narrower half of the same
// header: only the delta-seconds form is read, because converting an HTTP-date
// means trusting the collector's clock against ours.
func TestOTLPHTTPIgnoresAnHTTPDateRetryAfter(t *testing.T) {
	t.Parallel()
	header := http.Header{"Retry-After": []string{"Wed, 21 Oct 2026 07:28:00 GMT"}}
	exporter, _ := collectorStub(t, http.StatusServiceUnavailable, "", header)
	err := exporter.Export(gaugeSnapshot(nil, 1))
	if !hasField(err, "retry_after_seconds", "0") {
		t.Errorf("an HTTP-date must report no hint rather than a guessed one: %v", err)
	}
}

// TestOTLPHTTPDoesNotFollowARedirect pins the CWE-918 gate. A 30x from anything
// in front of the collector would otherwise bounce the POST — Authorization
// header included — at whatever host the response names, past an allowlist that
// only ever saw the configured endpoint.
func TestOTLPHTTPDoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	var elsewhereHits int
	elsewhere := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		elsewhereHits++
	}))
	t.Cleanup(elsewhere.Close)
	header := http.Header{"Location": []string{elsewhere.URL + "/v1/metrics"}}
	exporter, _ := collectorStub(t, http.StatusTemporaryRedirect, "", header)
	err := exporter.Export(gaugeSnapshot(nil, 1))
	if !errors.Is(err, svcmetrics.OTLPExportRejected) {
		t.Fatalf("an unfollowed redirect is a permanent rejection, got %v", err)
	}
	if elsewhereHits != 0 {
		t.Errorf("the payload reached the redirect target %d time(s)", elsewhereHits)
	}
}

// TestOTLPHTTPTransportFaultIsRetryable pins "All Other Responses": "If the
// server disconnects without returning a response, the client SHOULD retry".
//
// The collector does exactly that — takes the connection and closes it without
// a byte of answer — and stays up for the whole test. The test used to close
// its server first and dial the port it had just freed, and a freed loopback
// port is handed to one of the next 50 listeners 0.8 % of the time (measured:
// 160 in 20 000), so a parallel test's server could answer in its place and a
// test that never runs its own server fail for no reason of its own.
func TestOTLPHTTPTransportFaultIsRetryable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hangUp(w)
	}))
	t.Cleanup(server.Close)
	endpoint := server.URL + svcmetrics.OTLPMetricsPath
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: endpoint,
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	exportErr := exporter.Export(gaugeSnapshot(nil, 1))
	if !errors.Is(exportErr, svcmetrics.OTLPExportUnavailable) {
		t.Fatalf("want OTLPExportUnavailable, got %v", exportErr)
	}
	if !svcmetrics.OTLPRetryable(exportErr) {
		t.Errorf("a transport fault is retryable")
	}
	if strings.Contains(exportErr.Error(), endpoint) {
		t.Errorf("the endpoint must not reach the rendered message: %v", exportErr)
	}
}

// TestOTLPHTTPRefusesAnUnencodableSnapshotBeforeDialing pins the boundary
// between the two surfaces: an encoder refusal never becomes a request.
func TestOTLPHTTPRefusesAnUnencodableSnapshotBeforeDialing(t *testing.T) {
	t.Parallel()
	exporter, captured := collectorStub(t, http.StatusOK, "", nil)
	snap := coremetrics.SnapshotValue{Sums: map[string]coremetrics.SumMetricValue{
		"s": {Temporality: coremetrics.TemporalityUnspecified, Points: []coremetrics.SumValue{{Value: 1}}},
	}}
	if err := exporter.Export(snap); !errors.Is(err, svcmetrics.OTLPUnresolvedTemporality) {
		t.Fatalf("want OTLPUnresolvedTemporality, got %v", err)
	}
	if seen := captured.snapshot(); seen.seen != 0 {
		t.Errorf("an encoder refusal reached the network %d time(s)", seen.seen)
	}
}

// TestOTLPHTTPSendsConfiguredHeaders pins the auth seam, and the rule that
// Content-Type is set LAST: the specification requires application/json for
// this encoding, so a caller cannot mislabel the body by accident.
func TestOTLPHTTPSendsConfiguredHeaders(t *testing.T) {
	t.Parallel()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.record(r)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	headers := map[string]string{"Authorization": "Bearer s3cret", "Content-Type": "text/plain"}
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
		Headers:  headers,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	//: mutating the caller's map afterwards must not change the wire.
	headers["Authorization"] = "Bearer changed"
	if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
		t.Fatalf("Export: unexpected error: %v", exportErr)
	}
	seen := captured.snapshot()
	if got := seen.headers.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization = %q, want the value configured at construction", got)
	}
	if got := seen.headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want the specification's value to win", got)
	}
}

// TestOTLPHTTPBoundsTheResponseBody pins the one length a REMOTE party controls
// in this exchange. A collector answering with a gigabyte must cost bytes, not
// the process.
func TestOTLPHTTPBoundsTheResponseBody(t *testing.T) {
	t.Parallel()
	//: a 200 whose body is far larger than the cap; the cap truncates the read
	//: mid-JSON, so the body becomes unparseable and the 200 stands.
	flood := `{"partialSuccess":{"errorMessage":"` + strings.Repeat("x", 4096) + `"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		writeStub(w, flood)
	}))
	t.Cleanup(server.Close)
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint:         server.URL + svcmetrics.OTLPMetricsPath,
		MaxResponseBytes: 16,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
		t.Errorf("a truncated 200 body leaves the acceptance standing, got %v", exportErr)
	}
}

// TestOTLPHTTPReturnsFromACollectorThatNeverStopsSending pins the one read the
// bounded-response promise (ADR 0048 §7) had missed: the drain that recycles the
// connection after the verdict. The client is server.Client(), which has NO
// timeout — a caller-supplied client is used as-is — so nothing but the
// exporter's own bounds can end the export. Two framings: chunked with no end,
// and a declared length the body never reaches, which net/http's own post-close
// drain does not even attempt, so the exporter's bound is all there is.
//
// Seen failing: with the drain back to an unbounded io.Copy, both cases ran out
// the 30s budget and printed
//
//	Export has not returned 30s into a response body that never ends; the drain after the verdict is unbounded
//
// and the suite finished rather than hung, because the budget severs the socket.
//
// Goroutine lifecycle: one exporter goroutine per case, ending when Export
// returns. The case receives on `returned` on both paths — on the budget path
// only after severing the socket, which is what makes Export return — so the
// receive joins it and nothing outlives the case. The channel is buffered, so
// the send never parks either way.
func TestOTLPHTTPReturnsFromACollectorThatNeverStopsSending(t *testing.T) {
	t.Parallel()
	framings := []struct {
		name          string
		contentLength string
	}{
		{name: "chunked"},
		{name: "declared length", contentLength: strconv.FormatInt(1<<40, 10)},
	}
	for _, framing := range framings {
		t.Run(framing.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if framing.contentLength != "" {
					w.Header().Set("Content-Length", framing.contentLength)
				}
				w.WriteHeader(http.StatusOK)
				streamForever(w, r)
			}))
			t.Cleanup(server.Close)
			exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
				Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
				Client:   server.Client(),
			})
			if err != nil {
				t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
			}
			budget, cancel := context.WithTimeout(t.Context(), endlessBodyBudget)
			t.Cleanup(cancel)
			returned := make(chan error, 1)
			go func() { returned <- exporter.Export(gaugeSnapshot(nil, 1)) }()
			select {
			case exportErr := <-returned:
				if exportErr != nil {
					t.Errorf("a 200 whose body never ends is still a 200; the bounds must not change the verdict, got %v", exportErr)
				}
			case <-budget.Done():
				//: sever the socket, so the stuck export and the handler feeding
				//: it end with this test instead of outliving it.
				server.CloseClientConnections()
				<-returned
				t.Fatalf("Export has not returned %v into a response body that never ends; "+
					"the drain after the verdict is unbounded", endlessBodyBudget)
			}
		})
	}
}

// TestOTLPHTTPReusesTheConnectionAcrossExports proves the bound did not break the
// one thing the drain is for: handing the connection back to the keep-alive
// pool, which net/http does only once it has seen the response end.
//
// Two collectors. A conforming one, whose few bytes the verdict's own read
// consumes to the end, so the drain finds nothing: the baseline. And one padding
// a conforming document with the whitespace JSON allows to half a cap PAST the
// read cap: the verdict's read stops short, so only the drain can reach the end,
// and the declared length is above the 256 KiB net/http drains by itself after
// an early Close (its maxPostCloseReadBytes; it does not try for a response
// declaring more), so nothing else can save the connection.
//
// The assertion is "fewer connections than exports", not "exactly one": net/http
// declines to recycle a connection whose request write it has not seen confirmed
// within 50 ms of reading the response, which is scheduling rather than this
// exporter, and Go's own suite raises that grace to an hour for exactly that
// reason. What the drain decides is whether reuse happens at all; without it,
// every export opens a connection of its own.
//
// Seen failing: with closeOTLPResponse reduced to the Close alone, the padded
// case printed, in 10 runs out of 10,
//
//	3 exports opened 3 connections; not one found its predecessor's idle, so the drain never reached the end of the response
//
// while the conforming case passed, its end already read by the verdict's read.
// With the drain in place, each of 10 runs opened exactly one.
func TestOTLPHTTPReusesTheConnectionAcrossExports(t *testing.T) {
	t.Parallel()
	conforming := `{"partialSuccess":{}}`
	padded := int(svcmetrics.DefaultOTLPMaxResponseBytes + svcmetrics.DefaultOTLPMaxResponseBytes/2)
	collectors := []struct {
		name string
		body string
	}{
		{name: "conforming response", body: conforming},
		{name: "padded past the read cap", body: conforming + strings.Repeat(" ", padded-len(conforming))},
	}
	for _, collector := range collectors {
		t.Run(collector.name, func(t *testing.T) {
			t.Parallel()
			server, connections := countingCollector(t, collector.body)
			exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
				Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
				//: its own transport: every httptest.Server.Close empties
				//: http.DefaultTransport's idle pool, so a parallel test's cleanup
				//: would close the very connection this one is watching.
				Client: server.Client(),
			})
			if err != nil {
				t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
			}
			for range reuseExports {
				if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
					t.Fatalf("Export: unexpected error: %v", exportErr)
				}
			}
			if opened := connections.Load(); opened >= reuseExports {
				t.Fatalf("%d exports opened %d connections; not one found its predecessor's idle, so the "+
					"drain never reached the end of the response", reuseExports, opened)
			}
		})
	}
}

// TestOTLPHTTPDefaultClientOwnsItsConnectionPool pins the fix for a verdict that
// came back wrong under load. The default client used to ride
// http.DefaultTransport, which every httptest.Server.Close — and any
// http.DefaultClient.CloseIdleConnections in the process — empties; and
// net/http puts a bodiless response's connection back in that pool BEFORE
// handing the response over, so an emptying landing in between reported
// "connection broken" for an answer that had already arrived. A 200 became a
// retryable OTLP_EXPORT_UNAVAILABLE, and a retry would replay accepted data.
// With the whole file in parallel and the OS threads oversubscribed it failed 4
// times in 800 runs, across TestOTLPHTTPSurfacesRetryAfter,
// TestOTLPHTTPIgnoresAnHTTPDateRetryAfter and the Request_Entity_Too_Large case
// of TestOTLPHTTPNonRetryableStatuses — a permanent rejection turned retryable.
//
// That window is microseconds wide, so this test does not chase it: it proves
// the precondition is gone, deterministically. Each export is followed by the
// very call that caused it, on the process pool, and a later export must still
// find a connection an earlier one left — which only a pool the process cannot
// reach can offer. "Fewer connections than exports", for the reason
// TestOTLPHTTPReusesTheConnectionAcrossExports gives; the shared pool can never
// satisfy it, because every sweep closes the one connection it holds.
//
// Seen failing: with the default client's Transport removed, so that it rode
// http.DefaultTransport again, it printed, in 10 runs out of 10,
//
//	3 exports opened 3 connections; emptying the process pool closed the exporter's, so the default client still rides http.DefaultTransport
func TestOTLPHTTPDefaultClientOwnsItsConnectionPool(t *testing.T) {
	t.Parallel()
	//: bodiless, because that is the answer the race turned into a fault.
	server, connections := countingCollector(t, "")
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: server.URL + svcmetrics.OTLPMetricsPath,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	for export := range reuseExports {
		if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
			t.Fatalf("export %d: a bodiless 200 is an acceptance, got %v", export, exportErr)
		}
		//: what every httptest.Server.Close does to the process pool.
		http.DefaultClient.CloseIdleConnections()
	}
	if opened := connections.Load(); opened >= reuseExports {
		t.Fatalf("%d exports opened %d connections; emptying the process pool closed the exporter's, so "+
			"the default client still rides http.DefaultTransport", reuseExports, opened)
	}
}

// TestOTLPHTTPDefaultClientHonoursTheProxyEnvironment pins what the dedicated
// transport must not lose: the proxy environment. An exporter that stopped
// honouring HTTP_PROXY would fail exactly in the deployments that set one, and
// no test on loopback could notice — net/http never proxies a loopback address,
// and it reads the environment once per process. So the export runs in a child
// process started with HTTP_PROXY pointing at this test's server and an
// endpoint that cannot resolve: the proxy seeing the request is the only way the
// child can succeed. Both branches of newOTLPTransport are driven — the clone,
// and the fallback taken when http.DefaultTransport is no *http.Transport.
//
// Seen failing: with newOTLPTransport returning a bare &http.Transport{}, both
// cases printed
//
//	the child's export did not reach the collector through HTTP_PROXY: exit status 1
//
// above the child's own line naming why — it had tried to resolve the collector
// itself: `dial tcp: lookup collector.invalid …: no such host`. With the
// default client's Transport removed altogether, the fallback case failed too,
// the client having fallen through to the nil process default: `http: no
// Client.Transport or DefaultTransport`.
func TestOTLPHTTPDefaultClientHonoursTheProxyEnvironment(t *testing.T) {
	t.Parallel()
	modes := []struct {
		name      string
		transport string
	}{
		{name: "clone of http.DefaultTransport", transport: "clone"},
		{name: "fallback for a replaced http.DefaultTransport", transport: "fallback"},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			var forwarded atomic.Value
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				discardRequest(r)
				forwarded.Store(r.RequestURI)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(proxy.Close)
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOTLPHTTPProxyChild$") //nolint:gosec
			child.Env = append(withoutProxyEnvironment(os.Environ()),
				"HTTP_PROXY="+proxy.URL, proxyChildEnv+"="+mode.transport)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("the child's export did not reach the collector through HTTP_PROXY: %v\n%s", err, output)
			}
			if got, _ := forwarded.Load().(string); got != unresolvableEndpoint {
				t.Fatalf("the proxy forwarded %q, want %q", got, unresolvableEndpoint)
			}
		})
	}
}

// TestOTLPHTTPUsesASuppliedClientAsGiven pins the other half of the pool
// decision: only the DEFAULT client gets a transport of its own. A supplied
// client, Transport included, is the caller's choice — the seam for a proxy, an
// mTLS identity or an SSRF allowlist — and reaches the wire untouched. Its
// RoundTripper here answers the request itself and the endpoint cannot resolve,
// so an SDK that replaced, wrapped or cloned that Transport would fail the
// export instead of passing it through.
//
// Seen failing: with NewOTLPHTTPExporter giving a supplied client the
// dedicated transport too, it printed
//
//	Export through the supplied client: [0.3.45.9 OTLP_EXPORT_UNAVAILABLE] The OTLP collector is unreachable or overloaded (cause: …)
//
// — the request had left for the network instead of reaching the supplied
// RoundTripper: through the host's egress proxy, which answered a 5xx for the
// unresolvable name (on a host without one, the lookup itself fails).
func TestOTLPHTTPUsesASuppliedClientAsGiven(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})}
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: unresolvableEndpoint,
		Client:   client,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
		t.Fatalf("Export through the supplied client: %v (cause: %v)", exportErr, errors.Unwrap(exportErr))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("the supplied client's Transport answered %d requests, want 1", got)
	}
}

// TestOTLPHTTPProxyChild is the child of
// TestOTLPHTTPDefaultClientHonoursTheProxyEnvironment: one export with the
// default client, through whatever proxy the parent put in the environment. It
// skips unless the parent set proxyChildEnv, so `go test ./...` discovers it,
// runs it, and it costs nothing.
func TestOTLPHTTPProxyChild(t *testing.T) {
	mode := os.Getenv(proxyChildEnv)
	if mode == "" {
		t.Skip("not the proxy child: " + proxyChildEnv + " is unset")
	}
	if mode == "fallback" {
		//: a process default that is no *http.Transport — nil here, so a
		//: fallback that routed through it would fail rather than pass.
		http.DefaultTransport = nil
	}
	exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{
		Endpoint: unresolvableEndpoint,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: unexpected error: %v", err)
	}
	if exportErr := exporter.Export(gaugeSnapshot(nil, 1)); exportErr != nil {
		t.Fatalf("the %s transport did not take the export through HTTP_PROXY: %v (cause: %v)",
			mode, exportErr, errors.Unwrap(exportErr))
	}
}

// withoutProxyEnvironment returns env minus every variable net/http reads to
// choose a proxy, so the child sees exactly the one its parent sets.
func withoutProxyEnvironment(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "REQUEST_METHOD":
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// hangUp takes the connection from under net/http and closes it without writing
// a byte: a collector that disconnects without returning a response. The
// outcome cannot change what the test asserts — the client sees the hang-up
// either way — so the helper names the discard instead of hiding it.
func hangUp(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	if closeErr := conn.Close(); closeErr != nil {
		return
	}
}

// countingCollector starts a collector answering every request with a 200 that
// carries body under a declared Content-Length, and counts the connections it
// accepts.
func countingCollector(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	connections := &atomic.Int32{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		//: the whole request first, so the client's write is over before the
		//: answer starts; net/http recycles nothing it is still writing to.
		discardRequest(r)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		writeStub(w, body)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	return server, connections
}

// streamForever writes a response body until the client goes away: what a
// broken or hostile collector looks like from the exporter's side — a status
// line that promised an answer, then bytes that never end.
func streamForever(w http.ResponseWriter, r *http.Request) {
	chunk := []byte(strings.Repeat("x", 32<<10))
	for r.Context().Err() == nil {
		if _, err := w.Write(chunk); err != nil {
			return
		}
	}
}

// discardRequest reads a request body to its end. The outcome cannot change
// what the test asserts, so the helper names the discard instead of hiding it.
func discardRequest(r *http.Request) {
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return
	}
}

// TestNewOTLPHTTPExporterRefusesAnUnusableEndpoint pins the construction gate.
// The path check is the one that earns its keep: a bare host CONNECTS, answers
// 404, and looks exactly like a collector that is up.
func TestNewOTLPHTTPExporterRefusesAnUnusableEndpoint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		endpoint string
	}{
		{"empty", ""},
		{"no scheme", "collector:4318/v1/metrics"},
		{"wrong scheme", "grpc://collector:4318/v1/metrics"},
		{"no host", "http:///v1/metrics"},
		{"no path", "http://collector:4318"},
		{"root path only", "http://collector:4318/"},
		{"unparseable", "http://collector:4318/v1/metrics\x7f\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			exporter, err := svcmetrics.NewOTLPHTTPExporter("otlp-http-test", svcmetrics.OTLPHTTPConfig{Endpoint: test.endpoint})
			if !errors.Is(err, svcmetrics.OTLPEndpointInvalid) {
				t.Fatalf("want OTLPEndpointInvalid, got %v", err)
			}
			if exporter != nil {
				t.Errorf("a refused construction must return no exporter")
			}
			if !kerrs.HasCode(err, svcmetrics.CodeOTLPEndpointInvalid) {
				t.Errorf("want code 0.3.45.7 on the trail, got %v", err)
			}
		})
	}
}

// TestOTLPHTTPExporterIsNotRegistered pins the deliberate absence. The registry
// is reached by importing a package, and arming a network client from an import
// is strictly worse than the stdout hazard ADR 0030 already refuses.
func TestOTLPHTTPExporterIsNotRegistered(t *testing.T) {
	t.Parallel()
	for _, name := range coremetrics.AvailableExporters() {
		if strings.Contains(string(name), "http") {
			t.Errorf("no OTLP/HTTP exporter may self-register, found %q", name)
		}
	}
}

// TestOTLPHTTPExporterNameRoundTrips pins the trivial half of the port.
func TestOTLPHTTPExporterNameRoundTrips(t *testing.T) {
	t.Parallel()
	exporter, _ := collectorStub(t, http.StatusOK, "", nil)
	if exporter.Name() != coremetrics.ExporterName("otlp-http-test") {
		t.Errorf("Name = %q, want otlp-http-test", exporter.Name())
	}
}

// hasField reports whether err carries key with the given rendered value.
func hasField(err error, key, value string) bool {
	var typed *kerrs.Error
	if !errors.As(err, &typed) {
		return false
	}
	for _, field := range typed.Fields() {
		if field.Key() == key && field.StringValue() == value {
			return true
		}
	}
	return false
}

// writeStub writes a collector stub's canned body. The write outcome cannot
// change what the test asserts — the exporter's verdict comes from the status
// line — so the helper names the discard instead of hiding it in a blank.
func writeStub(w http.ResponseWriter, body string) {
	if _, err := io.WriteString(w, body); err != nil {
		return
	}
}
