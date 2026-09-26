// Package health_test — Ask against real loopback servers, one per outcome.
// Every budget here runs on a manual clock: the package's audit forbids a
// wall-clock wait in its tests as much as in its code.
package health_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/health"
)

// bodySecret is planted in every body a not-ready process answers, so a test
// can prove no error ever repeats one.
const bodySecret string = "BODY-SECRET-5f1c9e"

// askBudget is the budget the manual-clock tests arm and then advance past.
const askBudget time.Duration = 2 * time.Second

// serveOnLoopback starts a loopback HTTP server answering handler and returns its port.
func serveOnLoopback(t *testing.T, handler http.Handler) (port string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	//: httptest always binds a host:port.
	if err != nil {
		t.Fatalf("server address %q: %v", server.Listener.Addr(), err)
	}
	return port
}

// frozen is a manual clock nobody advances: a budget armed on it never fires,
// so the outcome a test asserts cannot be a timeout it did not ask for.
func frozen() *clock.ManualClock {
	//: any instant; only its not moving matters.
	return clock.NewManualClock(time.Unix(0, 0))
}

// renderings returns every text an error can be read as: its Public and
// Private halves, every field, and everything its chain unwraps to.
func renderings(err error) []string {
	texts := []string{kerrs.PublicOf(err), kerrs.PrivateOf(err)}
	//: every field, as text.
	for _, field := range kerrs.FieldsOf(err) {
		texts = append(texts, field.StringValue())
	}
	//: every link of the chain.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		texts = append(texts, cause.Error())
	}
	return texts
}

// fieldOf returns the value of the named field an error carries.
func fieldOf(err error, key string) (string, bool) {
	//: the first field of that name.
	for _, field := range kerrs.FieldsOf(err) {
		//: found.
		if field.Key() == key {
			return field.StringValue(), true
		}
	}
	return "", false
}

// TestAskIsReadyOnlyOnTwoHundred pins the verdict: 200 is ready and nothing
// else is, the status comes back either way, and a not-ready answer's error
// carries the status and never a byte of the body.
func TestAskIsReadyOnlyOnTwoHundred(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		status int
	}
	tests := []tc{
		{"200 is ready", http.StatusOK},
		{"204 is not", http.StatusNoContent},
		{"503 is not", http.StatusServiceUnavailable},
		{"500 is not", http.StatusInternalServerError},
		{"404 is not", http.StatusNotFound},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		port := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.status)
			//: a body the error must never repeat.
			if c.status != http.StatusNoContent {
				//: a client that left early is not this test's failure.
				if _, err := io.WriteString(w, `{"status":"`+bodySecret+`"}`); err != nil {
					t.Logf("body write: %v", err)
				}
			}
		}))
		status, err := health.Ask(t.Context(), health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz", Clock: frozen()})
		//: the status answered, whatever it was.
		if status != c.status {
			t.Errorf("status = %d, want %d", status, c.status)
		}
		//: ready exactly on 200.
		if c.status == http.StatusOK {
			//: no error at all.
			if err != nil {
				t.Fatalf("Ask() error = %v, want nil", err)
			}
			return
		}
		//: every other status is AskNotReady.
		if !errors.Is(err, health.AskNotReady) {
			t.Fatalf("Ask() error = %v, want ASK_NOT_READY", err)
		}
		//: the status travels in the error too.
		if got, ok := fieldOf(err, "status"); !ok || got != strconv.Itoa(c.status) {
			t.Errorf("status field = %q (%v), want %d", got, ok, c.status)
		}
		//: and the body never does.
		for _, text := range renderings(err) {
			//: any rendering quoting the body is the defect.
			if strings.Contains(text, bodySecret) {
				t.Fatalf("an error repeated the body: %q", text)
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

// TestAskFollowsNoRedirect pins that a redirect is an answer: the probe asked
// this process, so a Location naming another path — or another host — is
// reported, never followed.
func TestAskFollowsNoRedirect(t *testing.T) {
	t.Parallel()
	var followed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	})
	mux.HandleFunc("/elsewhere", func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusOK)
	})
	port := serveOnLoopback(t, mux)
	status, err := health.Ask(t.Context(), health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz", Clock: frozen()})
	//: the redirect itself is the answer.
	if status != http.StatusFound || !errors.Is(err, health.AskNotReady) {
		t.Fatalf("Ask() = %d, %v; want 302 and ASK_NOT_READY", status, err)
	}
	//: and its target was never asked.
	if followed.Load() != 0 {
		t.Fatalf("the redirect was followed %d time(s)", followed.Load())
	}
}

// TestAskIsUnreachableWhenNothingListens pins the unreachable outcome: a port
// nothing listens on refuses the connection, and the transport's error stays
// in the chain. The clock is frozen, so however long a platform takes to
// report the refusal, the verdict cannot be a timeout.
func TestAskIsUnreachableWhenNothingListens(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	//: a free port, taken and released.
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	//: nothing listens there any more.
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	status, err := health.Ask(t.Context(), health.AskConfig{Addr: address, Path: "/readyz", Clock: frozen()})
	//: no answer, and why.
	if status != 0 || !errors.Is(err, health.AskUnreachable) {
		t.Fatalf("Ask() = %d, %v; want 0 and ASK_UNREACHABLE", status, err)
	}
	//: the dial's own error is kept for whoever logs the chain.
	if opErr, isOp := errors.AsType[*net.OpError](err); !isOp || opErr == nil {
		t.Errorf("the chain lost the transport's error: %v", err)
	}
	//: and the address it tried is named.
	if got, _ := fieldOf(err, "target"); got != address {
		t.Errorf("target field = %q, want %q", got, address)
	}
}

// TestAskTimesOutOnItsOwnBudget drives the budget on a manual clock: a process
// that accepts the request and never answers is reported as ASK_TIMEOUT the
// moment the clock passes the budget — not a moment of real time later.
//
// Goroutine lifecycle: one goroutine runs the Ask and publishes its outcome on
// a buffered channel the test receives from; it ends when the budget fires,
// which the test drives, so none outlives the test.
func TestAskTimesOutOnItsOwnBudget(t *testing.T) {
	t.Parallel()
	arrived := make(chan struct{}, 1)
	port := serveOnLoopback(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	clk := frozen()
	type outcome struct {
		status int
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		status, err := health.Ask(context.Background(), health.AskConfig{
			Addr: "127.0.0.1:" + port, Path: "/readyz", Timeout: askBudget, Clock: clk,
		})
		done <- outcome{status: status, err: err}
	}()
	<-arrived
	clk.BlockUntil(1)
	clk.Advance(askBudget)
	got := <-done
	//: no answer, because of the budget.
	if got.status != 0 || !errors.Is(got.err, health.AskTimeout) {
		t.Fatalf("Ask() = %d, %v; want 0 and ASK_TIMEOUT", got.status, got.err)
	}
	//: the budget it ran out of is named.
	if budget, _ := fieldOf(got.err, "budget"); budget != askBudget.String() {
		t.Errorf("budget field = %q, want %q", budget, askBudget)
	}
}

// TestAskEndsWithItsCallersContext pins the other bound: the caller's context
// ends the exchange too, and its own error stays in the chain.
//
// Goroutine lifecycle: one goroutine runs the Ask and publishes its error on a
// buffered channel; it ends when the test cancels the context it was given.
func TestAskEndsWithItsCallersContext(t *testing.T) {
	t.Parallel()
	arrived := make(chan struct{}, 1)
	port := serveOnLoopback(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := health.Ask(ctx, health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz", Clock: frozen()})
		done <- err
	}()
	<-arrived
	cancel()
	err := <-done
	//: a timeout, as a departed caller's check is.
	if !errors.Is(err, health.AskTimeout) {
		t.Fatalf("Ask() error = %v, want ASK_TIMEOUT", err)
	}
	//: caused by the caller's own context.
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the chain lost the caller's context error: %v", err)
	}
}

// TestAskWithAnExpiredDeadlineAsksNothing pins that a caller's deadline that
// already passed ends the Ask at once, with context.DeadlineExceeded in the
// chain and no request reaching the process.
func TestAskWithAnExpiredDeadlineAsksNothing(t *testing.T) {
	t.Parallel()
	var asked atomic.Int32
	port := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	_, err := health.Ask(ctx, health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz", Clock: frozen()})
	//: a timeout caused by the deadline.
	if !errors.Is(err, health.AskTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Ask() error = %v, want ASK_TIMEOUT caused by DeadlineExceeded", err)
	}
	//: nothing was asked.
	if asked.Load() != 0 {
		t.Errorf("the process was asked %d time(s)", asked.Load())
	}
}

// TestAskDrainsABoundedBody pins the drain bound: a ready process whose body
// never ends does not hold the probe. Reading the body to its end — what
// io.ReadAll does — would never return here; Ask reads at most
// MaxAskDrainBytes, closes, and reports ready.
func TestAskDrainsABoundedBody(t *testing.T) {
	t.Parallel()
	stopped := make(chan struct{})
	port := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(stopped)
		w.WriteHeader(http.StatusOK)
		chunk := []byte(strings.Repeat("x", 32<<10))
		//: until the client hangs up.
		for {
			//: a write fails once the probe closed the connection.
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	status, err := health.Ask(t.Context(), health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz", Clock: frozen()})
	//: the status is the answer.
	if status != http.StatusOK || err != nil {
		t.Fatalf("Ask() = %d, %v; want 200 and nil", status, err)
	}
	//: and the connection was closed behind it.
	<-stopped
}

// TestAskDialsTheLoopbackForAnUnspecifiedHost pins the mapping a HEALTHCHECK
// relies on: a process given ":port" or "0.0.0.0:port" to listen on is asked
// on 127.0.0.1, one given "[::]:port" on ::1. Windows refuses a connection to
// an unspecified address outright, and a URL with no host is no URL at all.
func TestAskDialsTheLoopbackForAnUnspecifiedHost(t *testing.T) {
	t.Parallel()
	ready := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	port := serveOnLoopback(t, ready)
	for _, listen := range []string{":" + port, "0.0.0.0:" + port, "[::ffff:0.0.0.0]:" + port} {
		status, err := health.Ask(t.Context(), health.AskConfig{Addr: listen, Path: "/readyz", Clock: frozen()})
		//: answered over IPv4's loopback.
		if status != http.StatusOK || err != nil {
			t.Errorf("Ask(%q) = %d, %v; want 200 and nil", listen, status, err)
		}
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	//: a host with no IPv6 loopback cannot show the IPv6 half.
	if err != nil {
		t.Skipf("no IPv6 loopback on this host (%v): the [::] mapping is pinned by the internal table", err)
	}
	server := httptest.NewUnstartedServer(ready)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	_, port6, err := net.SplitHostPort(listener.Addr().String())
	//: a bound listener always has a host:port.
	if err != nil {
		t.Fatalf("listener address: %v", err)
	}
	status, err := health.Ask(t.Context(), health.AskConfig{Addr: "[::]:" + port6, Path: "/readyz", Clock: frozen()})
	//: answered over IPv6's loopback.
	if status != http.StatusOK || err != nil {
		t.Errorf("Ask([::]:%s) = %d, %v; want 200 and nil", port6, status, err)
	}
}

// TestAskRefusesAConfigurationBeforeDialling pins ASK_MISCONFIGURED: each
// configuration no answer could satisfy is refused, naming the argument, and
// the live process behind the address is never asked.
func TestAskRefusesAConfigurationBeforeDialling(t *testing.T) {
	t.Parallel()
	var asked atomic.Int32
	port := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	live := "127.0.0.1:" + port
	type tc struct {
		name     string
		cfg      health.AskConfig
		argument string
	}
	tests := []tc{
		{"no port", health.AskConfig{Addr: "127.0.0.1", Path: "/readyz"}, "addr"},
		{"no address", health.AskConfig{Addr: "", Path: "/readyz"}, "addr"},
		{"port zero", health.AskConfig{Addr: "127.0.0.1:0", Path: "/readyz"}, "addr"},
		{"port past 65535", health.AskConfig{Addr: "127.0.0.1:65536", Path: "/readyz"}, "addr"},
		{"a named port", health.AskConfig{Addr: "127.0.0.1:http", Path: "/readyz"}, "addr"},
		{"a relative path", health.AskConfig{Addr: live, Path: "readyz"}, "path"},
		{"no path", health.AskConfig{Addr: live, Path: ""}, "path"},
		{"a URL for a path", health.AskConfig{Addr: live, Path: "http://127.0.0.1/readyz"}, "path"},
		{"a control byte in the path", health.AskConfig{Addr: live, Path: "/ready\x7f"}, "path"},
		{"a negative timeout", health.AskConfig{Addr: live, Path: "/readyz", Timeout: -time.Second}, "timeout"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		status, err := health.Ask(t.Context(), c.cfg)
		//: refused, with no status.
		if status != 0 || !errors.Is(err, health.AskMisconfigured) {
			t.Fatalf("Ask() = %d, %v; want 0 and ASK_MISCONFIGURED", status, err)
		}
		//: naming what is wrong, never its value.
		if got, _ := fieldOf(err, "argument"); got != c.argument {
			t.Errorf("argument field = %q, want %q", got, c.argument)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
	//: the process behind the one live address was never asked.
	if asked.Load() != 0 {
		t.Errorf("a refused configuration still asked the process %d time(s)", asked.Load())
	}
}

// TestAskSendsTheQueryItWasGiven pins that a path may carry a query, and that
// what arrives is that path — the host always the one dialled.
func TestAskSendsTheQueryItWasGiven(t *testing.T) {
	t.Parallel()
	seen := make(chan string, 1)
	port := serveOnLoopback(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.RequestURI()
		w.WriteHeader(http.StatusOK)
	}))
	_, err := health.Ask(t.Context(), health.AskConfig{Addr: "127.0.0.1:" + port, Path: "/readyz?verbose=1", Clock: frozen()})
	//: ready.
	if err != nil {
		t.Fatalf("Ask() error = %v", err)
	}
	//: the path and its query, unchanged.
	if got := <-seen; got != "/readyz?verbose=1" {
		t.Errorf("the process was asked %q, want /readyz?verbose=1", got)
	}
}
