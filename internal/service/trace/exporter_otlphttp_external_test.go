package trace_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/trace"
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
const proxyChildEnv string = "KTN_OTLP_TRACE_PROXY_CHILD"

// unresolvableEndpoint is a collector address that never resolves (".invalid",
// RFC 2606), so an export to it can only succeed through something other than
// DNS: the proxy named in the proxy child's environment, or a supplied client's
// own RoundTripper.
const unresolvableEndpoint string = "http://collector.invalid:4318" + svctrace.OTLPTracesPath

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
//
// The collector disconnects without answering — takes the connection and closes
// it — and stays up for the whole test. The test used to close its server first
// and dial the port it had just freed, and a freed loopback port is handed to
// one of the next 50 listeners 0.8 % of the time (measured: 160 in 20 000) — on
// a CI host running other test binaries, one of THEIR servers could answer here.
func TestOTLPHTTPReportsATransportFaultAsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hangUp(w)
	}))
	defer server.Close()
	endpoint := server.URL + svctrace.OTLPTracesPath

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

// TestOTLPHTTPReturnsFromACollectorThatNeverStopsSending pins the one read the
// "bounded response read (1 MiB)" of ADR 0051 had missed: the drain that
// recycles the connection after the verdict. The client is server.Client(),
// which has NO timeout — a caller-supplied client is used as-is — so nothing but
// the exporter's own bounds can end the export. Two framings: chunked with no
// end, and a declared length the body never reaches, which net/http's own
// post-close drain does not even attempt, so the exporter's bound is all there
// is.
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
			exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
				Endpoint: server.URL + svctrace.OTLPTracesPath,
				Client:   server.Client(),
			})
			if err != nil {
				t.Fatalf("NewOTLPHTTPExporter: %v", err)
			}
			//: built here, because fixtureSpans may stop the test and only this
			//: goroutine is allowed to.
			batch := fixtureSpans(t)
			budget, cancel := context.WithTimeout(t.Context(), endlessBodyBudget)
			t.Cleanup(cancel)
			returned := make(chan error, 1)
			go func() { returned <- exporter.Export(batch) }()
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
	padded := int(svctrace.DefaultOTLPMaxResponseBytes + svctrace.DefaultOTLPMaxResponseBytes/2)
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
			exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
				Endpoint: server.URL + svctrace.OTLPTracesPath,
				//: its own transport: every httptest.Server.Close empties
				//: http.DefaultTransport's idle pool, so a parallel test's cleanup
				//: would close the very connection this one is watching.
				Client: server.Client(),
			})
			if err != nil {
				t.Fatalf("NewOTLPHTTPExporter: %v", err)
			}
			batch := fixtureSpans(t)
			for range reuseExports {
				if exportErr := exporter.Export(batch); exportErr != nil {
					t.Fatalf("Export: %v", exportErr)
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
// came back wrong under load — found in the metrics sibling, whose tests run in
// parallel, and present here in the same code. The default client used to ride
// http.DefaultTransport, which every httptest.Server.Close — and any
// http.DefaultClient.CloseIdleConnections in the process — empties; and
// net/http puts a bodiless response's connection back in that pool BEFORE
// handing the response over, so an emptying landing in between reported
// "connection broken" for an answer that had already arrived. A 200 became a
// retryable OTLP_EXPORT_UNAVAILABLE, and a retry would replay accepted spans.
// This file's older tests never showed it only because none of them runs in
// parallel.
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
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
		Endpoint: server.URL + svctrace.OTLPTracesPath,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	batch := fixtureSpans(t)
	for export := range reuseExports {
		if exportErr := exporter.Export(batch); exportErr != nil {
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
				readAll(r)
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
//	Export through the supplied client: [0.3.50.7 OTLP_EXPORT_UNAVAILABLE] The trace collector is unavailable (cause: …)
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
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{
		Endpoint: unresolvableEndpoint,
		Client:   client,
	})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	if exportErr := exporter.Export(fixtureSpans(t)); exportErr != nil {
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
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: unresolvableEndpoint})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	if exportErr := exporter.Export(fixtureSpans(t)); exportErr != nil {
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
		readAll(r)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		answer(w, body)
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

// answer writes a collector's canned body. Unlike writeString it does not report
// a fault: it runs on the server's goroutine, and a client that stopped reading
// is a consequence the calling test asserts on, not a failure of its own.
func answer(w io.Writer, body string) {
	if _, err := io.WriteString(w, body); err != nil {
		return
	}
}
