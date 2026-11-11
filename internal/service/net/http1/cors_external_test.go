package http1_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

var corsCfg = corenet.CORSValue{
	Origins:       []string{"https://app.example", "https://b.example:8443"},
	Methods:       []string{"GET", "POST", "PUT"},
	Headers:       []string{"Content-Type", "X-Token"},
	ExposeHeaders: []string{"X-Total"},
	MaxAge:        corenet.DurationValue(10 * time.Minute),
}

// corsServe sends one request through the CORS link over a handler that marks
// having run, and returns the response.
func corsServe(t *testing.T, cfg corenet.CORSValue, raw string) (*corenet.HTTP1ResponseValue, bool) {
	t.Helper()
	link, err := http1.CORS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var reached bool
	h := link(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		reached = true
		res.Body = []byte("x")
	}))
	var res corenet.HTTP1ResponseValue
	h.ServeHTTP1(parse(t, raw), &res)
	return &res, reached
}

func corsReq(method, origin string, extra ...string) string {
	b := method + " /api HTTP/1.1\r\nHost: h\r\n"
	if origin != "" {
		b += "Origin: " + origin + "\r\n"
	}
	for _, e := range extra {
		b += e + "\r\n"
	}
	return b + "\r\n"
}

func head(t *testing.T, res *corenet.HTTP1ResponseValue) string {
	t.Helper()
	out, err := http1.AppendResponse(nil, &corenet.HTTP1RequestValue{Method: []byte("GET")}, res, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestCORS_preflightListed(t *testing.T) {
	res, reached := corsServe(t, corsCfg, corsReq("OPTIONS", "https://app.example", "Access-Control-Request-Method: POST"))
	if reached {
		t.Fatal("a preflight reached the handler")
	}
	if res.Status != 204 {
		t.Fatalf("status %d", res.Status)
	}
	got := head(t, res)
	for _, want := range []string{
		"Access-Control-Allow-Origin: https://app.example\r\n",
		"Vary: Origin\r\n",
		"Access-Control-Max-Age: 600\r\n",
		"Access-Control-Allow-Methods: GET, POST, PUT\r\n",
		"Access-Control-Allow-Headers: Content-Type, X-Token\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Access-Control-Allow-Credentials") {
		t.Errorf("credentials offered:\n%s", got)
	}
}

func TestCORS_preflightRefusals(t *testing.T) {
	for _, c := range []struct {
		name, raw string
	}{
		{"unlisted origin", corsReq("OPTIONS", "https://evil.example", "Access-Control-Request-Method: POST")},
		{"unlisted method", corsReq("OPTIONS", "https://app.example", "Access-Control-Request-Method: DELETE")},
		{"origin differing by case", corsReq("OPTIONS", "https://APP.example", "Access-Control-Request-Method: POST")},
		{"origin with a suffix", corsReq("OPTIONS", "https://app.example.evil.net", "Access-Control-Request-Method: POST")},
		{"the literal null", corsReq("OPTIONS", "null", "Access-Control-Request-Method: POST")},
	} {
		res, reached := corsServe(t, corsCfg, c.raw)
		if reached || res.Status != 204 {
			t.Errorf("%s: reached %v, status %d", c.name, reached, res.Status)
		}
		got := head(t, res)
		if strings.Contains(strings.ToLower(got), "access-control") {
			t.Errorf("%s: a CORS field:\n%s", c.name, got)
		}
		if !strings.Contains(got, "Vary: Origin\r\n") {
			t.Errorf("%s: no Vary:\n%s", c.name, got)
		}
	}
}

func TestCORS_actualRequests(t *testing.T) {
	res, reached := corsServe(t, corsCfg, corsReq("GET", "https://b.example:8443"))
	if !reached {
		t.Fatal("not served")
	}
	got := head(t, res)
	for _, want := range []string{"Access-Control-Allow-Origin: https://b.example:8443\r\n", "Vary: Origin\r\n", "Access-Control-Expose-Headers: X-Total\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	//: an unlisted origin is served all the same, the browser enforces: no 403, no field.
	res, reached = corsServe(t, corsCfg, corsReq("GET", "https://evil.example"))
	if !reached || res.Status != 0 || strings.Contains(strings.ToLower(head(t, res)), "access-control") {
		t.Fatalf("unlisted: reached %v, status %d\n%s", reached, res.Status, head(t, res))
	}
	//: no Origin at all: the answer still varies on it.
	res, _ = corsServe(t, corsCfg, corsReq("GET", ""))
	if got := head(t, res); !strings.Contains(got, "Vary: Origin\r\n") || strings.Contains(strings.ToLower(got), "access-control") {
		t.Fatalf("no origin:\n%s", got)
	}
	//: OPTIONS without Access-Control-Request-Method is an ordinary request.
	if _, reached := corsServe(t, corsCfg, corsReq("OPTIONS", "https://app.example")); !reached {
		t.Fatal("a plain OPTIONS did not reach the handler")
	}
}

func TestCORS_wildcardAndCredentials(t *testing.T) {
	res, _ := corsServe(t, corenet.CORSValue{Origins: []string{"*"}}, corsReq("GET", "https://anything.example"))
	if got := head(t, res); !strings.Contains(got, "Access-Control-Allow-Origin: *\r\n") {
		t.Fatalf("wildcard:\n%s", got)
	}
	res, _ = corsServe(t, corenet.CORSValue{Origins: []string{"https://a.example"}, Credentials: true}, corsReq("GET", "https://a.example"))
	if got := head(t, res); !strings.Contains(got, "Access-Control-Allow-Credentials: true\r\n") || !strings.Contains(got, "Access-Control-Allow-Origin: https://a.example\r\n") {
		t.Fatalf("credentials:\n%s", got)
	}
}

func TestCORS_maxAgeClamped(t *testing.T) {
	res, _ := corsServe(t, corenet.CORSValue{Origins: []string{"https://a.example"}, MaxAge: corenet.DurationValue(72 * time.Hour)},
		corsReq("OPTIONS", "https://a.example", "Access-Control-Request-Method: GET"))
	if got := head(t, res); !strings.Contains(got, "Access-Control-Max-Age: 86400\r\n") {
		t.Fatalf("%s", got)
	}
}

// TestCORS_defaultMethods holds the safe default: CORS-safelisted methods only.
func TestCORS_defaultMethods(t *testing.T) {
	cfg := corenet.CORSValue{Origins: []string{"https://a.example"}}
	if res, _ := corsServe(t, cfg, corsReq("OPTIONS", "https://a.example", "Access-Control-Request-Method: POST")); !strings.Contains(head(t, res), "Access-Control-Allow-Methods: GET, HEAD, POST\r\n") {
		t.Fatalf("%s", head(t, res))
	}
	if res, _ := corsServe(t, cfg, corsReq("OPTIONS", "https://a.example", "Access-Control-Request-Method: PATCH")); strings.Contains(head(t, res), "Access-Control") {
		t.Fatalf("%s", head(t, res))
	}
}

func TestCORS_refusedAtConstruction(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  corenet.CORSValue
		want string
	}{
		{"wildcard with credentials", corenet.CORSValue{Origins: []string{"*"}, Credentials: true}, "origins"},
		{"no origin", corenet.CORSValue{}, "origins"},
		{"wildcard among others", corenet.CORSValue{Origins: []string{"*", "https://a.example"}}, "origins"},
		{"the null origin", corenet.CORSValue{Origins: []string{"null"}}, "origins"},
		{"a path", corenet.CORSValue{Origins: []string{"https://a.example/x"}}, "origins"},
		{"a trailing slash", corenet.CORSValue{Origins: []string{"https://a.example/"}}, "origins"},
		{"no scheme", corenet.CORSValue{Origins: []string{"a.example"}}, "origins"},
		{"upper case", corenet.CORSValue{Origins: []string{"https://A.example"}}, "origins"},
		{"control byte", corenet.CORSValue{Origins: []string{"https://a\r\nX: y"}}, "origins"},
		{"a repeated origin", corenet.CORSValue{Origins: []string{"https://a.example", "https://a.example"}}, "origins"},
		{"a method not a token", corenet.CORSValue{Origins: []string{"https://a.example"}, Methods: []string{"G T"}}, "methods"},
		{"a header not a token", corenet.CORSValue{Origins: []string{"https://a.example"}, Headers: []string{"A:B"}}, "headers"},
		{"an exposed header not a token", corenet.CORSValue{Origins: []string{"https://a.example"}, ExposeHeaders: []string{""}}, "expose_headers"},
		{"a negative max age", corenet.CORSValue{Origins: []string{"https://a.example"}, MaxAge: -1}, "max_age"},
	} {
		_, err := http1.CORS(c.cfg)
		if !errors.Is(err, corenet.CORSMisconfigured) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !slices.ContainsFunc(errs.FieldsOf(err), func(f errs.FieldValue) bool {
			return f.Key() == "option" && f.StringValue() == c.want
		}) {
			t.Errorf("%s: %v, want option=%q", c.name, err, c.want)
		}
	}
}

// TestCORS_onDeferredAndPendingAnswers holds that the fields survive a
// deferred answer and a worker's own response value.
func TestCORS_onDeferredAndPendingAnswers(t *testing.T) {
	link, err := http1.CORS(corsCfg)
	if err != nil {
		t.Fatal(err)
	}
	a, out := startPend(pendingWorkerOf{HTTP1Handler: link(pendingIDH{})}, 1<<16)
	io := newFakeIO(0)
	ref := corenet.LoopConnRef{Slot: 1, Gen: 1}
	a.OnOpen(io, ref)
	raw := corsReq("GET", "https://app.example")
	if n := a.OnData(io, ref, []byte(raw)); n != len(raw) {
		t.Fatal(n)
	}
	a.OnBatchEnd(io)
	finish(a, io, out)
	a.OnBatchEnd(io)
	if !strings.Contains(string(io.out[ref]), "Access-Control-Allow-Origin: https://app.example\r\n") {
		t.Fatalf("%q", io.out[ref])
	}
}
