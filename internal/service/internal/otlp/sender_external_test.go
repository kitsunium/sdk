// Package otlp_test — the shared OTLP/HTTP sender against a real listener: the
// three verdicts, the decode of a partial success, and the property the shared
// transport exists around — every verdict, every refusal, carries the CALLING
// signal's code and nothing of this package's own.
package otlp_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/internal/otlp"
)

// testPath is the signal path every test endpoint carries.
const testPath string = "/v1/test"

// testRejectedField is the field key the stand-in signal counts a loss under.
const testRejectedField string = "rejected_items"

// testPartialSuccess is the stand-in signal's partialSuccess message: one
// member, as each real signal's is, under a name only it uses.
type testPartialSuccess struct {
	RejectedItems otlp.LenientInt64 `json:"rejectedItems"`
}

// RejectedCount implements otlp.RejectedCounter.
func (p *testPartialSuccess) RejectedCount() int64 { return int64(p.RejectedItems) }

// decodeTestRejected is the stand-in signal's SignalSpec.DecodeRejected.
func decodeTestRejected(payload []byte) int64 {
	var message testPartialSuccess
	return otlp.DecodeRejected(payload, &message)
}

// specFor builds a stand-in signal whose every code shares one MM.LL.PP, so a
// test can tell two signals' verdicts apart by the package byte alone.
func specFor(pkg errs.PkgCode) *otlp.SignalSpec {
	code := func(serial errs.Serial) errs.Code { return errs.Pack(0, 3, pkg, serial) }
	return &otlp.SignalSpec{
		EndpointInvalid: errs.Define(code(1), "TEST_ENDPOINT_INVALID",
			"The test endpoint is unusable", "otlp_test: stand-in endpoint refusal"),
		EndpointUnparsable: errs.WrapParams{
			Code: code(1), Reason: "TEST_ENDPOINT_INVALID",
			Public: "The test endpoint is unusable", Private: "otlp_test: unparsable endpoint",
		},
		RequestUnbuildable: errs.WrapParams{
			Code: code(2), Reason: "TEST_REJECTED",
			Public: "The test collector rejected it", Private: "otlp_test: unbuildable request",
		},
		TransportFault: errs.WrapParams{
			Code: code(3), Reason: "TEST_UNAVAILABLE",
			Public: "The test collector is unavailable", Private: "otlp_test: transport fault",
		},
		Rejected: errs.Define(code(2), "TEST_REJECTED",
			"The test collector rejected it", "otlp_test: stand-in rejection"),
		Unavailable: errs.Define(code(3), "TEST_UNAVAILABLE",
			"The test collector is unavailable", "otlp_test: stand-in transient verdict"),
		PartialSuccess: errs.Define(code(4), "TEST_PARTIAL_SUCCESS",
			"The test collector dropped some items", "otlp_test: stand-in partial success"),
		RejectedField:  testRejectedField,
		DecodeRejected: decodeTestRejected,
	}
}

// fieldValue returns the rendered value of the field key on err, if any.
func fieldValue(err error, key string) (value string, found bool) {
	var typed *errs.Error
	if !errors.As(err, &typed) {
		return "", false
	}
	for _, field := range typed.Fields() {
		if field.Key() == key {
			return field.StringValue(), true
		}
	}
	return "", false
}

// collector answers every POST with status, header and body, after reading the
// request whole so a keep-alive connection is reusable.
func collector(t *testing.T, status int, header map[string]string, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("the collector could not read the request: %v", err)
		}
		for key, value := range header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(status)
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("the collector could not answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestEveryVerdictCarriesTheCallersCode is the seam, end to end: the same
// exchange, carried for two signals, ends in two verdicts that differ in code
// and in nothing else — the sender decides WHICH verdict, the signal decides
// what it is CALLED.
func TestEveryVerdictCarriesTheCallersCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		status int
		header map[string]string
		body   string
		serial errs.Serial
		field  string
		value  string
	}
	tests := []tc{
		{name: "a permanent rejection", status: http.StatusBadRequest, serial: 2, field: "http_status", value: "400"},
		{name: "a 500 is permanent too", status: http.StatusInternalServerError, serial: 2, field: "http_status", value: "500"},
		{
			name: "a throttled answer keeps its Retry-After", status: http.StatusTooManyRequests,
			header: map[string]string{"Retry-After": "30"}, serial: 3, field: "retry_after_seconds", value: "30",
		},
		{
			name: "an HTTP-date Retry-After is no hint", status: http.StatusServiceUnavailable,
			header: map[string]string{"Retry-After": "Wed, 21 Oct 2015 07:28:00 GMT"}, serial: 3,
			field: "retry_after_seconds", value: "0",
		},
		{
			name: "a partial success is a loss", status: http.StatusOK,
			body: `{"partialSuccess":{"rejectedItems":"7"}}`, serial: 4, field: testRejectedField, value: "7",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		server := collector(t, c.status, c.header, c.body)
		for _, pkg := range []errs.PkgCode{0xF1, 0xF2} {
			sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: server.URL + testPath}, specFor(pkg))
			if err != nil {
				t.Fatalf("NewSender: %v", err)
			}
			verdict := sender.Post([]byte(`{}`))
			if want := errs.Pack(0, 3, pkg, c.serial); !errs.HasCode(verdict, want) {
				t.Errorf("signal %#x: verdict %v, want its own %s", pkg, verdict, want)
			}
			if got, found := fieldValue(verdict, c.field); !found || got != c.value {
				t.Errorf("signal %#x: field %s = %q (%v), want %q", pkg, c.field, got, found, c.value)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestASuccessIsNil pins the other side of the 2xx branch: an empty body, an
// explicit zero in either spelling, an explicit null, and a body that is not
// the expected message at all are each a FULL success — inventing a loss from
// a malformed answer would report a failure that did not happen.
func TestASuccessIsNil(t *testing.T) {
	t.Parallel()
	bodies := map[string]string{
		"an empty body":         "",
		"a zero as a string":    `{"partialSuccess":{"rejectedItems":"0"}}`,
		"a zero as a number":    `{"partialSuccess":{"rejectedItems":0}}`,
		"an explicit null":      `{"partialSuccess":null}`,
		"an unparseable body":   `not json`,
		"a non-numeric count":   `{"partialSuccess":{"rejectedItems":"many"}}`,
		"another signal's name": `{"partialSuccess":{"rejectedSpans":"5"}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server := collector(t, http.StatusOK, nil, body)
			sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: server.URL + testPath}, specFor(0xF1))
			if err != nil {
				t.Fatalf("NewSender: %v", err)
			}
			if verdict := sender.Post([]byte(`{}`)); verdict != nil {
				t.Errorf("Post = %v, want a full success", verdict)
			}
		})
	}
}

// TestAnUnusableEndpointIsRefusedAtConstruction pins the refusal at wiring,
// under the caller's code — including the one the path check exists for: a bare
// host connects, answers 404, and looks exactly like a collector that is up.
func TestAnUnusableEndpointIsRefusedAtConstruction(t *testing.T) {
	t.Parallel()
	endpoints := map[string]string{
		"empty":               "",
		"no scheme":           "collector:4318/v1/test",
		"not http":            "ftp://collector/v1/test",
		"no host":             "http:///v1/test",
		"a bare host":         "http://collector:4318",
		"the root path":       "http://collector:4318/",
		"a malformed address": "http://[::1",
	}
	for name, endpoint := range endpoints {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: endpoint}, specFor(0xF2))
			if sender != nil || !errs.HasCode(err, errs.Pack(0, 3, 0xF2, 1)) {
				t.Errorf("NewSender(%q) = %v, %v; want the caller's endpoint refusal", endpoint, sender, err)
			}
		})
	}
}

// TestATransportFaultIsTheTransientVerdict pins the verdict for a request that
// never got an answer: the caller's TransportFault wrap, which carries the
// Unavailable code a retry classifier reads.
func TestATransportFaultIsTheTransientVerdict(t *testing.T) {
	t.Parallel()
	server := collector(t, http.StatusOK, nil, "")
	endpoint := server.URL + testPath
	server.Close()
	sender, err := otlp.NewSender(&otlp.HTTPConfig{Endpoint: endpoint}, specFor(0xF1))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if verdict := sender.Post([]byte(`{}`)); !errs.HasCode(verdict, errs.Pack(0, 3, 0xF1, 3)) {
		t.Errorf("Post to a closed collector = %v, want the caller's transient code", verdict)
	}
}

// TestContentTypeIsTheLastWord pins the header order: a caller's headers ride
// along, and the Content-Type the specification requires is set after them, so
// it always wins.
func TestContentTypeIsTheLastWord(t *testing.T) {
	t.Parallel()
	seen := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	sender, err := otlp.NewSender(&otlp.HTTPConfig{
		Endpoint: server.URL + testPath,
		Headers:  map[string]string{"Authorization": "Bearer t", "Content-Type": "text/plain"},
	}, specFor(0xF1))
	if err != nil {
		t.Fatalf("NewSender: %v", err)
	}
	if verdict := sender.Post([]byte(`{}`)); verdict != nil {
		t.Fatalf("Post = %v", verdict)
	}
	header := <-seen
	if header.Get("Content-Type") != "application/json" || header.Get("Authorization") != "Bearer t" {
		t.Errorf("headers = %v, want the caller's plus application/json", header)
	}
}

// TestTheDefaultsAreTheSpecificationsOwn pins the two clamps' targets: the
// OTEL_EXPORTER_OTLP_TIMEOUT default, and a response cap finite next to a
// hostile collector and generous next to a conforming one.
func TestTheDefaultsAreTheSpecificationsOwn(t *testing.T) {
	t.Parallel()
	if otlp.DefaultTimeout.Seconds() != 10 {
		t.Errorf("DefaultTimeout = %v, want the specification's 10s", otlp.DefaultTimeout)
	}
	if otlp.DefaultMaxResponseBytes != 1<<20 {
		t.Errorf("DefaultMaxResponseBytes = %s, want 1 MiB", strconv.FormatInt(otlp.DefaultMaxResponseBytes, 10))
	}
}
