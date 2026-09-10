package trace_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
)

// TestOTLPHTTPExporterIsNeverRegistered is the assertion ADR 0051 §Decision 5
// exists to make checkable.
//
// The registry is reached by importing a package. Arming a WRITER that way is
// what ADR 0030 already refuses; arming a NETWORK CLIENT is a step further, and
// the reason is structural rather than a matter of degree: there is no endpoint
// that could be a correct default, so a registered emitter would POST at whatever
// answers on an address the caller never named. Inside a cluster that is a real
// host belonging to somebody else.
func TestOTLPHTTPExporterIsNeverRegistered(t *testing.T) {
	names := svctraceNames()
	for _, name := range names {
		if strings.Contains(string(name), "http") || strings.Contains(string(name), "otlphttp") {
			t.Errorf("an OTLP/HTTP emitter is in the registry as %q; it must be constructed explicitly", name)
		}
	}
	if !slices.Contains(names, coretrace.ExporterName("otlpjson")) {
		t.Errorf("the writer-bound otlpjson exporter should be registered; got %v", names)
	}
}

// svctraceNames returns the registered exporter names, after asserting that the
// writer-bound exporter's package-level registration has actually run — which is
// what makes the ABSENCE of the HTTP emitter a meaningful observation rather than
// a listing nothing ever populated.
func svctraceNames() []coretrace.ExporterName {
	if svctrace.OTLPJSON.Name() != "otlpjson" {
		panic("the registered OTLP/JSON exporter did not name itself")
	}
	return coretrace.AvailableExporters()
}

// TestNewOTLPHTTPExporterRefusesAnEndpointThatCannotBeACollector pins the
// construction-time refusal, and especially the PATH check — the one that earns
// its keep. Every other mistake surfaces as a connection error a reader
// recognises, while a bare "http://collector:4318" connects, answers 404, and
// looks exactly like a collector that is up.
func TestNewOTLPHTTPExporterRefusesAnEndpointThatCannotBeACollector(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{"empty", ""},
		{"relative", "/v1/traces"},
		{"no scheme", "collector:4318/v1/traces"},
		{"wrong scheme", "grpc://collector:4318/v1/traces"},
		{"no host", "http:///v1/traces"},
		{"no path", "http://collector:4318"},
		{"root path only", "http://collector:4318/"},
		{"unparseable", "http://col lector:4318/v1/traces"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: tc.endpoint})
			if !errors.Is(err, svctrace.OTLPEndpointInvalid) {
				t.Fatalf("want OTLPEndpointInvalid, got %v", err)
			}
			if exporter != nil {
				t.Error("a refused endpoint must construct nothing — not an always-failing exporter")
			}
		})
	}
}

// TestOTLPHTTPExportPostsTheEncodedDocument pins the emitter's whole job: the same
// bytes EncodeOTLPJSON produced, at the configured path, with the JSON
// content type and the caller's headers.
func TestOTLPHTTPExportPostsTheEncodedDocument(t *testing.T) {
	var gotPath, gotType, gotAuth, gotMethod string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotType, gotAuth = r.Header.Get("Content-Type"), r.Header.Get("Authorization")
		gotBody = readAll(r)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
		Endpoint: server.URL + svctrace.OTLPTracesPath,
		Headers:  map[string]string{"Authorization": "Bearer secret"},
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	batch := fixtureSpans(t)
	if exportErr := exporter.Export(batch); exportErr != nil {
		t.Fatalf("Export: %v", exportErr)
	}
	if gotMethod != http.MethodPost || gotPath != svctrace.OTLPTracesPath {
		t.Errorf("request = %s %s, want POST %s", gotMethod, gotPath, svctrace.OTLPTracesPath)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want the caller's header", gotAuth)
	}
	want, err := svctrace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if string(gotBody) != string(want) {
		t.Errorf("body = %s, want the encoder's bytes exactly (no trailing newline)", gotBody)
	}
}

// TestOTLPHTTPExportSkipsAnEmptyBatch pins the no-op. A request carrying zero
// spans costs a round trip to say nothing, and an export loop on a quiet service
// would make one every interval forever.
func TestOTLPHTTPExportSkipsAnEmptyBatch(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	if exportErr := exporter.Export(coretrace.SpansValue{}); exportErr != nil {
		t.Fatalf("an empty batch must be a clean no-op, got %v", exportErr)
	}
	if calls != 0 {
		t.Errorf("the exporter made %d requests for an empty batch, want 0", calls)
	}
}

// TestOTLPHTTPClassification pins the three verdicts against the specification's
// own three sections, INCLUDING the half a status-class shortcut gets wrong: 5xx
// is not retryable as a class, because a 500 or a 501 means the same request will
// fail the same way.
func TestOTLPHTTPClassification(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		sentinel  error
		retryable bool
	}{
		{"200 empty body", http.StatusOK, "", nil, false},
		{"200 zero rejected", http.StatusOK, `{"partialSuccess":{"rejectedSpans":"0"}}`, nil, false},
		{"200 unparseable body", http.StatusOK, `not json at all`, nil, false},
		{"200 partial success as a string", http.StatusOK, `{"partialSuccess":{"rejectedSpans":"3"}}`, svctrace.OTLPPartialSuccess, false},
		{"200 partial success as a number", http.StatusOK, `{"partialSuccess":{"rejectedSpans":3}}`, svctrace.OTLPPartialSuccess, false},
		{"429", http.StatusTooManyRequests, "", svctrace.OTLPExportUnavailable, true},
		{"502", http.StatusBadGateway, "", svctrace.OTLPExportUnavailable, true},
		{"503", http.StatusServiceUnavailable, "", svctrace.OTLPExportUnavailable, true},
		{"504", http.StatusGatewayTimeout, "", svctrace.OTLPExportUnavailable, true},
		{"400", http.StatusBadRequest, "", svctrace.OTLPExportRejected, false},
		{"404", http.StatusNotFound, "", svctrace.OTLPExportRejected, false},
		{"500 is NOT retryable", http.StatusInternalServerError, "", svctrace.OTLPExportRejected, false},
		{"501 is NOT retryable", http.StatusNotImplemented, "", svctrace.OTLPExportRejected, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				writeString(t, w, tc.body)
			}))
			defer server.Close()
			exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
			if err != nil {
				t.Fatalf("NewOTLPHTTPExporter: %v", err)
			}
			exportErr := exporter.Export(fixtureSpans(t))
			if tc.sentinel == nil {
				if exportErr != nil {
					t.Fatalf("want a clean success, got %v", exportErr)
				}
				return
			}
			if !errors.Is(exportErr, tc.sentinel) {
				t.Fatalf("want %v, got %v", tc.sentinel, exportErr)
			}
			if got := svctrace.OTLPRetryable(exportErr); got != tc.retryable {
				t.Errorf("OTLPRetryable = %v, want %v", got, tc.retryable)
			}
		})
	}
}

// TestOTLPHTTPDoesNotFollowARedirect pins CWE-918. A 30x from anything in front
// of the collector would otherwise bounce the POST — Authorization header
// included — at whatever host the response names, past an allowlist that only
// ever saw the configured endpoint. The unfollowed 30x is then classified as the
// permanent rejection a misconfigured endpoint is.
func TestOTLPHTTPDoesNotFollowARedirect(t *testing.T) {
	var elsewhereCalls int
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+svctrace.OTLPTracesPath, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
		Endpoint: redirector.URL + svctrace.OTLPTracesPath,
		Headers:  map[string]string{"Authorization": "Bearer secret"},
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	exportErr := exporter.Export(fixtureSpans(t))
	if !errors.Is(exportErr, svctrace.OTLPExportRejected) {
		t.Fatalf("an unfollowed redirect must classify as a permanent rejection, got %v", exportErr)
	}
	if elsewhereCalls != 0 {
		t.Errorf("the credentialled POST reached the redirect target %d times; it must never leave the configured host", elsewhereCalls)
	}
}

// TestOTLPHTTPReportsATransportFaultAsRetryable pins the last row of the table:
// "All Other Responses — the client SHOULD retry".
func TestOTLPHTTPReportsATransportFaultAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := server.URL + svctrace.OTLPTracesPath
	server.Close()

	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: endpoint})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	exportErr := exporter.Export(fixtureSpans(t))
	if !errors.Is(exportErr, svctrace.OTLPExportUnavailable) {
		t.Fatalf("want OTLPExportUnavailable, got %v", exportErr)
	}
	if !svctrace.OTLPRetryable(exportErr) {
		t.Error("a transport fault is the specification's own SHOULD-retry case")
	}
}

// TestOTLPHTTPRefusesAnUnencodableBatchBeforeTouchingTheNetwork pins the ordering
// the two-surface split buys: an encoding defect never becomes a network
// question.
func TestOTLPHTTPRefusesAnUnencodableBatchBeforeTouchingTheNetwork(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	batch := fixtureSpans(t)
	batch.Spans[0].Context.TraceID = coretrace.TraceID{}
	if exportErr := exporter.Export(batch); !errors.Is(exportErr, svctrace.OTLPInvalidSpanContext) {
		t.Fatalf("want OTLPInvalidSpanContext, got %v", exportErr)
	}
	if calls != 0 {
		t.Errorf("the exporter opened %d connections for a batch it could not encode", calls)
	}
}
