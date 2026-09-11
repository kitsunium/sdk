// Package metrics_test — the OTLP/HTTP emitter against a real socket.
//
// Every response the collector stub returns below is one the OTLP
// specification's "OTLP/HTTP Response" section describes, and every assertion
// is on the verdict that section prescribes — full success, partial success,
// a retryable status, a permanent rejection.
package metrics_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

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
func TestOTLPHTTPTransportFaultIsRetryable(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL + svcmetrics.OTLPMetricsPath
	//: close before exporting, so the dial itself fails.
	server.Close()
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
