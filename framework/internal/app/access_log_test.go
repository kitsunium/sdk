package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// Ticket is a product that answers, refuses and takes its time, for the
// access record's tests (#1003).
var Ticket = app.NewService("ticket", "Answers, refuses and takes its time, for the access record's tests.")

// TicketItem asks for one item.
type TicketItem struct {
	ID string `json:"-" path:"id"`
}

// TicketAnswer is an item and the request id its handler read.
type TicketAnswer struct {
	ID        string `json:"id"`
	RequestID string `json:"requestId"`
}

// ticketClock is the clock the ticket office runs on: a handler that takes its time
// moves it.
var ticketClock = clock.NewManualClock(time.Unix(1_700_000_000, 0))

var (
	_ = Ticket.Endpoint("GET /items/{id}", func(ctx context.Context, in TicketItem) (TicketAnswer, error) {
		return TicketAnswer{ID: in.ID, RequestID: app.RequestID(ctx)}, nil
	})
	_ = Ticket.Endpoint("GET /missing/{id}", func(context.Context, TicketItem) (TicketAnswer, error) {
		return TicketAnswer{}, app.NotFound("no such item")
	})
	_ = Ticket.Endpoint("GET /slow/{id}", func(_ context.Context, in TicketItem) (TicketAnswer, error) {
		ticketClock.Advance(150 * time.Millisecond)
		return TicketAnswer{ID: in.ID}, nil
	})
	_ = Ticket.Static("assets", "/assets/", ticketFiles, app.Root("."))
)

var ticketFiles = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>ticket</title>")}, "app.js": {Data: []byte("1")}}

// accessLogBuffer is a log destination the server's goroutines and the test share.
type accessLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *accessLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records are the JSON log records written so far whose message is msg.
func (b *accessLogBuffer) records(t *testing.T, msg string) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	for line := range strings.Lines(b.buf.String()) {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec["msg"] == msg || rec["message"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

// startTicket runs the ticket office in production, under the environment the caller
// set, with its logs in the returned buffer.
func startTicket(t *testing.T) (*app.App, *accessLogBuffer) {
	t.Helper()
	return startTicketIn(t, app.EnvProduction)
}

// startTicketIn is startTicket in the environment env.
func startTicketIn(t *testing.T, env string) (*app.App, *accessLogBuffer) {
	t.Helper()
	logs := &accessLogBuffer{}
	a := app.NewApp("ticket", Ticket).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Env(env),
		app.Analyze(false), app.Logs(logs), app.Clock(ticketClock))
	if err := a.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return a, logs
}

// get asks path with the headers given and answers the response and its body.
func accessGet(t *testing.T, a *app.App, path string, headers ...string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, a.URL()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, raw
}

// #1003: a served request writes one `request` record with what an operator
// asks of an access log, and answers with the request id it read or minted.
func TestAServedRequestWritesOneAccessRecord(t *testing.T) {
	a, logs := startTicket(t)
	resp, raw := accessGet(t, a, "/items/42", "X-Request-Id", "abc", "User-Agent", "probe/1.0")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("answered %d %s, want 200", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("X-Request-Id"); got != "abc" {
		t.Errorf("the response carries X-Request-Id %q, want the caller's abc", got)
	}
	if !strings.Contains(string(raw), `"requestId":"abc"`) {
		t.Errorf("app.RequestID(ctx) inside the handler is not abc: %s", raw)
	}
	recs := logs.records(t, "request")
	if len(recs) != 1 {
		t.Fatalf("%d request records, want exactly one: %q", len(recs), logs.buf.String())
	}
	rec := recs[0]
	for key, want := range map[string]any{"method": "GET", "route": "/items/{id}", "status": float64(200), "request_id": "abc", "user_agent": "probe/1.0", "client": "127.0.0.1"} {
		if rec[key] != want {
			t.Errorf("record %s = %v, want %v: %v", key, rec[key], want, rec)
		}
	}
	for _, key := range []string{"duration_ms", "bytes_in", "bytes_out", "trace_id", "span_id"} {
		if _, ok := rec[key]; !ok {
			t.Errorf("record has no %s: %v", key, rec)
		}
	}
	if n, _ := rec["bytes_out"].(float64); n <= 0 {
		t.Errorf("bytes_out = %v, want the body's length", rec["bytes_out"])
	}
	if id, _ := rec["trace_id"].(string); len(id) != 32 {
		t.Errorf("trace_id = %q, want the request's 32-hex trace", id)
	}
}

// A caller that sent no id, or one the framework does not accept, gets a
// minted UUIDv7 back — the raw path with its ids never reaches the record.
func TestARequestWithoutAUsableIDGetsAUUIDv7(t *testing.T) {
	a, logs := startTicket(t)
	for _, sent := range []string{"", "has space", strings.Repeat("a", 129), "newé"} {
		var headers []string
		if sent != "" {
			headers = []string{"X-Request-Id", sent}
		}
		resp, _ := accessGet(t, a, "/items/7", headers...)
		id := resp.Header.Get("X-Request-Id")
		if id == sent || len(id) != 36 || id[14] != '7' {
			t.Errorf("sent %q, answered %q, want a minted UUIDv7", sent, id)
		}
	}
	for _, rec := range logs.records(t, "request") {
		if rec["route"] != "/items/{id}" {
			t.Errorf("route = %v, want the pattern and not the path", rec["route"])
		}
	}
}

// An error answer carries the id a support ticket can quote, in its body and
// in the header.
func TestAnErrorBodyCarriesTheRequestID(t *testing.T) {
	a, _ := startTicket(t)
	resp, raw := accessGet(t, a, "/missing/9", "X-Request-Id", "ticket-1")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("answered %d, want 404", resp.StatusCode)
	}
	var body struct {
		Error struct {
			RequestID string `json:"requestId"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.RequestID != "ticket-1" || resp.Header.Get("X-Request-Id") != "ticket-1" {
		t.Errorf("body requestId %q, header %q, want ticket-1 in both", body.Error.RequestID, resp.Header.Get("X-Request-Id"))
	}
}

// <PRODUCT>_ACCESS_LOG says which requests are logged.
func TestTheAccessLogModes(t *testing.T) {
	cases := []struct {
		mode string
		want map[string]int
	}{
		{"all", map[string]int{"/items/1": 1, "/missing/1": 1, "/slow/1": 1}},
		{"errors", map[string]int{"/items/1": 0, "/missing/1": 1, "/slow/1": 0}},
		{"slow:100ms", map[string]int{"/items/1": 0, "/missing/1": 0, "/slow/1": 1}},
		{"off", map[string]int{"/items/1": 0, "/missing/1": 0, "/slow/1": 0}},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			t.Setenv("TICKET_ACCESS_LOG", c.mode)
			a, logs := startTicket(t)
			for path := range c.want {
				accessGet(t, a, path)
			}
			// The probes are never logged, whatever the mode.
			accessGet(t, a, "/_app/health/ready")
			counts := map[string]int{}
			for _, rec := range logs.records(t, "request") {
				route, _ := rec["route"].(string)
				if strings.HasPrefix(route, "/_app/") {
					t.Errorf("a probe was logged: %v", rec)
				}
				counts[route]++
			}
			for path, n := range c.want {
				route := path[:strings.LastIndex(path, "/")] + "/{id}"
				if counts[route] != n {
					t.Errorf("%s: %d records for %s, want %d (%v)", c.mode, counts[route], path, n, counts)
				}
			}
		})
	}
}

// An access mode that is not one refuses the start, naming the variable.
func TestAnInvalidAccessLogRefusesTheStart(t *testing.T) {
	for _, bad := range []string{"verbose", "slow:", "slow:fast", "slow:-1s"} {
		t.Setenv("TICKET_ACCESS_LOG", bad)
		a := app.NewApp("ticket", Ticket).With(app.InMemory(), app.Listen("127.0.0.1:0"), app.Analyze(false), app.Logs(io.Discard))
		err := a.Start(t.Context())
		if err == nil {
			_ = a.Stop(t.Context())
			t.Errorf("TICKET_ACCESS_LOG=%q: the start was not refused", bad)
			continue
		}
		if !errs.HasCode(err, app.CodeAppConfig) || !strings.Contains(errs.PublicOf(err), "TICKET_ACCESS_LOG") {
			t.Errorf("TICKET_ACCESS_LOG=%q: refused with %v, which does not name the variable", bad, err)
		}
	}
}

// A static file request carries the request id, and continues the caller's
// trace as an endpoint's request does.
func TestAStaticRequestCarriesTheIDAndTheTrace(t *testing.T) {
	a, logs := startTicket(t)
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	resp, _ := accessGet(t, a, "/assets/app.js", "X-Request-Id", "static-1", "traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Request-Id") != "static-1" {
		t.Fatalf("answered %d with X-Request-Id %q, want 200 and static-1", resp.StatusCode, resp.Header.Get("X-Request-Id"))
	}
	recs := logs.records(t, "request")
	if len(recs) != 1 || recs[0]["request_id"] != "static-1" || recs[0]["trace_id"] != traceID {
		t.Fatalf("the static request's record: %v, want request_id static-1 and the caller's trace %s", recs, traceID)
	}
}

// In dev the Studio shows every request, so the default record is the errors'.
func TestInDevOnlyTheErrorsAreLoggedByDefault(t *testing.T) {
	a, logs := startTicketIn(t, app.EnvDev)
	accessGet(t, a, "/items/1")
	accessGet(t, a, "/missing/1")
	// Dev writes text, not JSON: the routes the lines name are the test.
	logs.mu.Lock()
	text := logs.buf.String()
	logs.mu.Unlock()
	if !strings.Contains(text, "/missing/{id}") || strings.Contains(text, "/items/{id}") {
		t.Fatalf("dev wrote %q, want the 404's record alone", text)
	}
}

// The request's identifier is the http.request_id of its span, an endpoint's
// and a static file's alike, and a static file's span joins the trace the
// caller's traceparent names.
func TestTheRequestIDIsOnTheSpan(t *testing.T) {
	a, _ := startTicketIn(t, app.EnvDev)
	const callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	accessGet(t, a, "/assets/app.js", "X-Request-Id", "static-2", "traceparent", "00-"+callerTrace+"-00f067aa0ba902b7-01")
	resp, _ := accessGet(t, a, "/items/5", "X-Request-Id", "endpoint-2")
	parts := strings.Split(resp.Header.Get("traceparent"), "-")
	if len(parts) != 4 {
		t.Fatalf("the endpoint's response has no traceparent: %v", resp.Header)
	}
	for _, c := range []struct{ trace, id string }{{callerTrace, "static-2"}, {parts[1], "endpoint-2"}} {
		var tr model.Trace
		eventually(t, "the trace of "+c.id, func() bool {
			r, raw := accessGet(t, a, "/_app/api/traces/"+c.trace)
			return r.StatusCode == http.StatusOK && json.Unmarshal(raw, &tr) == nil
		})
		var seen bool
		for _, s := range tr.Spans {
			seen = seen || s.Attrs["http.request_id"] == c.id
		}
		if !seen {
			t.Errorf("no span of trace %s carries http.request_id %s: %+v", c.trace, c.id, tr.Spans)
		}
	}
}
