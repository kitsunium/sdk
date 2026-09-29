package kit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

func errorsAs(err error, target **kit.Error) bool {
	for e := err; e != nil; {
		if ke, ok := e.(*kit.Error); ok {
			*target = ke
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

func TestLogsAreKeptBesideTheirSpans(t *testing.T) {
	var terminal lockedBuffer
	app := startBench(t, kit.Logs(&terminal))
	stream := subscribe(t, app)
	r := call(t, app, "GET /hello?name=ann", noBody)
	if r.status != http.StatusNoContent {
		t.Fatalf("hello: %d %s", r.status, r.body)
	}
	tr := traceOf(t, app, r)
	span := spanOf(t, tr, "bench/endpoint/Hello")
	e := awaitEvent(t, stream, "the log event", func(e model.Event) bool {
		return e.Type == model.EventLog && e.Log.Message == "hello"
	})
	if e.Log.TraceID != tr.TraceID || e.Log.SpanID != span.SpanID || e.Log.Node != "bench/endpoint/Hello" || e.Log.Level != "info" {
		t.Fatalf("the record does not name its span: %+v", e.Log)
	}
	a := e.Log.Attrs
	if a["user"] != "ann" || a["password"] != "[redacted]" || a["took"] != "1.5s" {
		t.Errorf("attrs %v", a)
	}
	if strings.Contains(a["creds"], "k-123") || !strings.Contains(a["creds"], `"user":"ann"`) {
		t.Errorf("a field tagged kit:\"secret\" leaked: %s", a["creds"])
	}
	if strings.Contains(a["dsn"], "pw@") {
		t.Errorf("a URL's credentials leaked: %s", a["dsn"])
	}
	if _, ok := a["app"]; ok {
		t.Error("the app attribute is noise")
	}

	var logs []model.LogRecord
	call(t, app, "GET /_kit/api/logs?trace="+tr.TraceID, noBody).json(t, &logs)
	var msgs []string
	for _, l := range logs {
		msgs = append(msgs, l.Level+":"+l.Message)
	}
	if !slices.Equal(msgs, []string{"debug:counting sheep", "info:hello", "warn:careful"}) {
		t.Fatalf("the trace's logs: %v", msgs)
	}
	if logs[0].Seq >= logs[1].Seq || logs[1].Seq >= logs[2].Seq {
		t.Error("records are not oldest first")
	}
	logs = nil
	call(t, app, "GET /_kit/api/logs?level=warn&node=bench/endpoint/Hello", noBody).json(t, &logs)
	if len(logs) != 1 || logs[0].Message != "careful" {
		t.Errorf("warn and above: %+v", logs)
	}
	logs = nil
	call(t, app, "GET /_kit/api/logs?limit=1", noBody).json(t, &logs)
	if len(logs) != 1 || logs[0].Message != "careful" {
		t.Errorf("the last record: %+v", logs)
	}
	for _, bad := range []string{"level=loud", "limit=0", "limit=x", "limit=5000"} {
		if r := call(t, app, "GET /_kit/api/logs?"+bad, noBody); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
	out := terminal.String()
	if !strings.Contains(out, "hello") || strings.Contains(out, "counting sheep") {
		t.Errorf("the terminal keeps its level (info), the Studio every level:\n%s", out)
	}
}

// lockedBuffer is a buffer the logger and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPayloadsAreKeptRedacted(t *testing.T) {
	app := startBench(t)
	r := call(t, app, "POST /signup", `{"email":"ann@example.com","password":"hunter2","hint":"cat's name","profile":{"name":"Ann","apiKey":"ak-1","recovery":"r-9"},"bio":"hi"}`, "Authorization", "Bearer abc.def")
	if r.status != http.StatusOK {
		t.Fatalf("signup: %d %s", r.status, r.body)
	}
	s := spanOf(t, traceOf(t, app, r), "bench/endpoint/SignUp")
	if s.Payload == nil {
		t.Fatal("no payload")
	}
	var in, out map[string]any
	if err := json.Unmarshal(s.Payload.Request, &in); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(s.Payload.Response, &out); err != nil {
		t.Fatal(err)
	}
	profile, _ := in["profile"].(map[string]any)
	for what, got := range map[string]any{
		"password": in["password"], "hint (kit:secret)": in["hint"], "Agent (the Authorization header)": in["Agent"],
		"profile.apiKey": profile["apiKey"], "profile.recovery (kit:secret)": profile["recovery"], "sessionToken": out["sessionToken"],
	} {
		if got != "[redacted]" {
			t.Errorf("%s = %v", what, got)
		}
	}
	if in["email"] != "ann@example.com" || profile["name"] != "Ann" || out["id"] != "acct_1" {
		t.Errorf("what is not secret is kept: %v %v", in, out)
	}
	raw := string(s.Payload.Request) + string(s.Payload.Response)
	for _, secret := range []string{"hunter2", "cat's name", "abc.def", "ak-1", "r-9", "tok-sensitive"} {
		if strings.Contains(raw, secret) {
			t.Errorf("%q leaked into the payload", secret)
		}
	}

	// Past 8 KiB, a side is cut — and stays JSON.
	r = call(t, app, "POST /signup", map[string]string{"email": "big@example.com", "bio": strings.Repeat("é", 20_000)})
	s = spanOf(t, traceOf(t, app, r), "bench/endpoint/SignUp")
	if !s.Payload.Truncated || len(s.Payload.Request) > 9<<10 || !json.Valid(s.Payload.Request) {
		t.Fatalf("a big payload: truncated %v, %d bytes, valid %v", s.Payload.Truncated, len(s.Payload.Request), json.Valid(s.Payload.Request))
	}

	// An error's body, a delivery's message.
	r = call(t, app, "GET /items/item_missing", noBody)
	s = spanOf(t, traceOf(t, app, r), "shop/endpoint/GetItem")
	if !strings.Contains(string(s.Payload.Response), `"not_found"`) || !strings.Contains(string(s.Payload.Request), `item_missing`) {
		t.Errorf("error payload %s / %s", s.Payload.Request, s.Payload.Response)
	}
	it := create(t, app, "carried", 1)
	eventually(t, "the delivery", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	var traces []model.Trace
	call(t, app, "GET /_kit/api/traces?root=shop/endpoint/CreateItem", noBody).json(t, &traces)
	found := false
	for _, tr := range traces {
		for _, sp := range tr.Spans {
			if sp.Node == "audit/subscription/record" && sp.Payload != nil && strings.Contains(string(sp.Payload.Request), it.ID) {
				found = true
			}
		}
	}
	if !found {
		t.Error("the delivery's span does not carry its message")
	}
}

// A delivery runs inside the trace of the request that published: it is
// found through its subscription, which roots no trace of its own.
func TestTracesThroughANode(t *testing.T) {
	app := startBench(t)
	it := create(t, app, "through", 1)
	eventually(t, "the delivery", func() bool {
		_, err := Log.Get(context.Background(), it.ID+".create")
		return err == nil
	})
	const sub = "audit/subscription/record"
	var through []model.Trace
	eventually(t, "the delivery's span", func() bool {
		call(t, app, "GET /_kit/api/traces?node="+sub, noBody).json(t, &through)
		return len(through) > 0
	})
	for _, tr := range through {
		if !slices.ContainsFunc(tr.Spans, func(s model.Span) bool { return s.Node == sub }) {
			t.Fatalf("trace %s does not go through %s", tr.TraceID, sub)
		}
	}
	if through[0].Root != "shop/endpoint/CreateItem" {
		t.Errorf("the delivery's trace starts at %q, want the request that published", through[0].Root)
	}
	var rooted []model.Trace
	call(t, app, "GET /_kit/api/traces?root="+sub, noBody).json(t, &rooted)
	if len(rooted) != 0 {
		t.Errorf("a subscription roots %d traces", len(rooted))
	}
	var both []model.Trace
	call(t, app, "GET /_kit/api/traces?root=shop/endpoint/CreateItem&node="+sub, noBody).json(t, &both)
	if len(both) == 0 || both[0].TraceID != through[0].TraceID {
		t.Errorf("rooted at the endpoint and through the subscription: %d traces", len(both))
	}
	if r := call(t, app, "GET /_kit/api/traces?node=nowhere", noBody); string(r.body) != "[]\n" {
		t.Errorf("through a node nothing went through: %s", r.body)
	}
}

// A call made on someone's behalf carries them on its span, and on the spans
// under it.
func TestSpansNameTheirUser(t *testing.T) {
	app := startBench(t)
	stream := subscribe(t, app)
	ctx := kit.WithUser(t.Context(), "user_42", struct{}{})
	out, err := BenchWhoAPI.Call(ctx, kit.EmptyValue{})
	if err != nil || out.User != "user_42" {
		t.Fatalf("who: %+v %v", out, err)
	}
	e := awaitEvent(t, stream, "the call's span", func(e model.Event) bool {
		return e.Type == model.EventSpan && e.Span.Node == "bench/endpoint/Who"
	})
	if e.Span.User != "user_42" {
		t.Errorf("span user %q", e.Span.User)
	}
}

// The dev tools do not exist outside dev: a production product keeps no
// log, no payload, no mock.
func TestProductionKeepsNothingForTheStudio(t *testing.T) {
	app := startBench(t, kit.Env(kit.EnvProduction))
	call(t, app, "GET /hello?name=ann", noBody)
	create(t, app, "quiet", 1)
	g := app.Graph()
	if g.Runtime.Mocks != nil {
		t.Errorf("production runtime %+v", g.Runtime)
	}
	if g.Runtime.HTTP == nil || g.Runtime.Process == nil || len(g.Runtime.History) == 0 {
		t.Errorf("the daemon describes itself in production too: %+v", g.Runtime)
	}
}
