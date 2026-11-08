package rpc_test

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	_ "github.com/kitsunium/sdk/internal/service/data/codec/json"
	_ "github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
	"github.com/kitsunium/sdk/internal/service/proc/ipc/rpc"
)

// testPeer is the peer a double's connections name.
var testPeer = coreipc.PeerValue{UID: 1000, GID: 1000, PID: 42, Verified: true}

// pipeDialer is a Dialer double: each Dial is a net.Pipe whose server end
// the server serves on a goroutine of its own, its verdict on served.
type pipeDialer struct {
	srv    *rpc.Server
	served chan error
}

// Dial returns the client end of a fresh pipe.
func (d *pipeDialer) Dial(context.Context) (*coreipc.Conn, error) {
	client, server := net.Pipe()
	go func() {
		d.served <- d.srv.ServeConn(context.Background(), &coreipc.Conn{Conn: server, Peer: testPeer})
	}()
	return &coreipc.Conn{Conn: client, Peer: testPeer}, nil
}

// serverConfig is a server's bounds for a test.
func serverConfig(inFlight int) rpc.ServerConfig {
	return rpc.ServerConfig{
		Formats: []codec.Format{"json", "msgpack"}, MaxFrameBytes: 64 << 10,
		MaxInFlight: inFlight, FrameTimeout: 5 * time.Second,
	}
}

// clientConfig is a client's bounds for a test.
func clientConfig(inFlight int) rpc.ClientConfig {
	return rpc.ClientConfig{
		Format: "json", MaxFrameBytes: 64 << 10, MaxInFlight: inFlight,
		FrameTimeout: 5 * time.Second, CallTimeout: 10 * time.Second,
	}
}

// pair starts a server over r and a client connected to it through a pipe.
func pair(t *testing.T, scfg rpc.ServerConfig, ccfg rpc.ClientConfig, r corerpc.Router) (*rpc.Server, *rpc.Client, *pipeDialer) {
	t.Helper()
	srv, err := rpc.NewServer(scfg, r)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	d := &pipeDialer{srv: srv, served: make(chan error, 1)}
	c, err := rpc.NewClient(context.Background(), d, ccfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		closeOrLog(t, c)
		closeOrLog(t, srv)
	})
	return srv, c, d
}

// closeOrLog closes c, logging a failure.
func closeOrLog(t *testing.T, c interface{ Close() error }) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

// echo answers with the request it decoded.
func echo(_ context.Context, req corerpc.Request) (any, error) {
	var in map[string]any
	if err := req.Decode(&in); err != nil {
		return nil, err
	}
	in["method"] = req.Method()
	in["pid"] = req.Peer().PID
	return in, nil
}

// waitFor polls cond until it holds or the test's patience runs out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCallRoundTripsAValueAndNamesThePeer(t *testing.T) {
	t.Parallel()
	_, c, _ := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{"echo": rpc.HandlerFunc(echo)})
	var out map[string]any
	if err := c.Call(context.Background(), "echo", map[string]any{"n": 7}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out["n"] != float64(7) || out["method"] != "echo" || out["pid"] != float64(42) {
		t.Fatalf("Call answered %v", out)
	}
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestACallerReadsTheServersRefusalsByCode(t *testing.T) {
	t.Parallel()
	_, c, _ := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{
		"sdk":   rpc.HandlerFunc(func(context.Context, corerpc.Request) (any, error) { return nil, coreipc.PeerRefused }),
		"plain": rpc.HandlerFunc(func(context.Context, corerpc.Request) (any, error) { return nil, errors.New("secret 1234") }),
	})
	type tc struct {
		method string
		want   errs.Code
	}
	for _, tt := range []tc{
		{"nope", corerpc.CodeMethodUnknown},
		{"sdk", coreipc.CodePeerRefused},
		{"plain", corerpc.CodeRemoteFailed},
	} {
		err := c.Call(context.Background(), tt.method, nil, nil)
		if !errs.HasCode(err, tt.want) {
			t.Fatalf("%s: got %v, want code %s", tt.method, err, tt.want)
		}
		if strings.Contains(err.Error(), "1234") || strings.Contains(errs.PrivateOf(err), "1234") {
			t.Fatalf("%s: the handler's own text crossed the socket: %v", tt.method, err)
		}
	}
}

func TestAStreamDeliversItsItemsThenItsEnd(t *testing.T) {
	t.Parallel()
	_, c, _ := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{
		"count": rpc.HandlerFunc(func(_ context.Context, req corerpc.Request) (any, error) {
			var n int
			if err := req.Decode(&n); err != nil {
				return nil, err
			}
			for i := range n {
				if err := req.Send(i); err != nil {
					return nil, err
				}
			}
			return n, nil
		}),
	})
	s, err := c.Stream(context.Background(), "count", 3)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer closeOrLog(t, s)
	var got []int
	for {
		var v int
		more, err := s.Recv(&v)
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if !more {
			break
		}
		got = append(got, v)
	}
	if len(got) != 4 || got[0] != 0 || got[2] != 2 || got[3] != 3 {
		t.Fatalf("the stream read %v, want [0 1 2 3]", got)
	}
}

func TestSendOnACallIsRefused(t *testing.T) {
	t.Parallel()
	_, c, _ := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{
		"x": rpc.HandlerFunc(func(_ context.Context, req corerpc.Request) (any, error) { return nil, req.Send(1) }),
	})
	if err := c.Call(context.Background(), "x", nil, nil); !errs.HasCode(err, corerpc.CodeProtocolViolation) {
		t.Fatalf("Send on a Call: %v, want PROTOCOL_VIOLATION", err)
	}
}

// TestThousandCallsAreBoundedByMaxInFlight is #930's acceptance: 1 000
// concurrent calls on one connection whose server runs at most 8 — the rest
// answered OVERLOADED at once, and the connection's goroutines on the server
// never more than MaxInFlight + 2.
func TestThousandCallsAreBoundedByMaxInFlight(t *testing.T) {
	const inFlight, calls = 8, 1000
	release := make(chan struct{})
	var running, peak atomic.Int64
	srv, c, _ := pair(t, serverConfig(inFlight), clientConfig(calls), rpc.Methods{
		"hold": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			n := running.Add(1)
			defer running.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			select {
			case <-release:
				return "done", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}),
	})
	var overloaded, done atomic.Int64
	var wg sync.WaitGroup
	for range calls {
		wg.Go(func() {
			err := c.Call(context.Background(), "hold", nil, nil)
			switch {
			case errs.HasCode(err, corerpc.CodeOverloaded):
				overloaded.Add(1)
			case err == nil:
				done.Add(1)
			default:
				t.Errorf("Call: %v", err)
			}
		})
	}
	waitFor(t, "every call beyond the eight answered OVERLOADED", func() bool {
		return overloaded.Load() == calls-inFlight && srv.InFlight() == inFlight
	})
	if got := serverConnGoroutines(); got > inFlight+2 {
		t.Fatalf("the server runs %d goroutines on the connection, want at most %d", got, inFlight+2)
	}
	close(release)
	wg.Wait()
	if done.Load() != inFlight || peak.Load() > inFlight {
		t.Fatalf("%d calls answered, %d handlers at once; want %d and at most %d", done.Load(), peak.Load(), inFlight, inFlight)
	}
}

// serverConnGoroutines counts the goroutines running the server's connection:
// its reader, its keeper and its calls.
func serverConnGoroutines() int {
	buf := make([]byte, 8<<20)
	//: a dump that filled the buffer may be cut: grow it until it fits.
	for n := runtime.Stack(buf, true); ; n = runtime.Stack(buf, true) {
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	n := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		//: the reader, a call's goroutine, or a keeper that is not a client's.
		if strings.Contains(g, "rpc.(*serverConn).serve") || strings.Contains(g, "rpc.(*serverCall).run") ||
			(strings.Contains(g, "rpc.(*keeper).run") && !strings.Contains(g, "rpc.NewClient")) {
			n++
		}
	}
	return n
}

// TestACancelledCallFreesItsSlot is #930's acceptance: with one slot, a call
// whose caller cancels sends the cancel frame, the handler's context ends with
// CALL_CANCELLED, the slot is freed, and the next call runs.
func TestACancelledCallFreesItsSlot(t *testing.T) {
	t.Parallel()
	causes := make(chan error, 1)
	srv, c, _ := pair(t, serverConfig(1), clientConfig(4), rpc.Methods{
		"block": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return nil, ctx.Err()
		}),
		"echo": rpc.HandlerFunc(echo),
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "block", nil, nil) }()
	waitFor(t, "the call to hold the one slot", func() bool { return srv.InFlight() == 1 })
	if err := c.Call(context.Background(), "echo", map[string]any{}, nil); !errs.HasCode(err, corerpc.CodeOverloaded) {
		t.Fatalf("a second call while the slot is held: %v, want OVERLOADED", err)
	}
	cancel()
	if err := <-errc; !errs.HasCode(err, corerpc.CodeCallCancelled) {
		t.Fatalf("the cancelled call: %v, want CALL_CANCELLED", err)
	}
	if cause := <-causes; !errs.HasCode(cause, corerpc.CodeCallCancelled) {
		t.Fatalf("the handler's context ended with %v, want CALL_CANCELLED", cause)
	}
	waitFor(t, "the slot to be freed", func() bool { return srv.InFlight() == 0 })
	if err := c.Call(context.Background(), "echo", map[string]any{}, nil); err != nil {
		t.Fatalf("the next call: %v", err)
	}
}

// TestACallPastItsTimeoutSendsTheCancelFrame is #930's acceptance: a call
// whose CallTimeout passes on the client's clock is CALL_TIMEOUT, and the
// server's handler sees the cancel frame for its id — its own clock never
// moved, so only the frame can have ended it.
func TestACallPastItsTimeoutSendsTheCancelFrame(t *testing.T) {
	t.Parallel()
	cclk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	scfg := serverConfig(4)
	scfg.Clock = clock.NewManualClock(time.Unix(1_700_000_000, 0))
	ccfg := clientConfig(4)
	ccfg.Clock, ccfg.CallTimeout = cclk, time.Second
	causes := make(chan error, 1)
	_, c, _ := pair(t, scfg, ccfg, rpc.Methods{
		"block": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return nil, ctx.Err()
		}),
	})
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "block", nil, nil) }()
	//: the client's clock also times its frames, so its pending waits are not
	//: the call's alone: it is moved a second at a time until the call gives up.
	if err := advanceUntil(t, cclk, time.Second, errc); !errs.HasCode(err, corerpc.CodeCallTimeout) {
		t.Fatalf("the call: %v, want CALL_TIMEOUT", err)
	}
	select {
	case cause := <-causes:
		if !errs.HasCode(cause, corerpc.CodeCallCancelled) {
			t.Fatalf("the handler's context ended with %v, want the cancel frame's CALL_CANCELLED", cause)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never saw the cancel frame")
	}
}

// advanceUntil moves clk by step, letting what it woke run, until errc
// answers.
func advanceUntil(t *testing.T, clk *clock.ManualClock, step time.Duration, errc <-chan error) error {
	t.Helper()
	for range 1000 {
		clk.Advance(step)
		select {
		case err := <-errc:
			return err
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("nothing answered while the clock moved")
	return nil
}

// TestTheBudgetEndsTheHandlerOnTheServersClock: the caller's deadline travels
// as a budget, which the server keeps on its own clock.
func TestTheBudgetEndsTheHandlerOnTheServersClock(t *testing.T) {
	t.Parallel()
	sclk := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	scfg := serverConfig(4)
	scfg.Clock = sclk
	started := make(chan struct{})
	_, c, _ := pair(t, scfg, clientConfig(4), rpc.Methods{
		"block": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, "block", nil, nil) }()
	<-started
	sclk.Advance(2 * time.Hour)
	if err := <-errc; !errs.HasCode(err, corerpc.CodeCallTimeout) {
		t.Fatalf("the call: %v, want the server's CALL_TIMEOUT", err)
	}
}

func TestAContextDeadlineIsCallTimeout(t *testing.T) {
	t.Parallel()
	_, c, _ := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{
		"block": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "block", nil, nil); !errs.HasCode(err, corerpc.CodeCallTimeout) {
		t.Fatalf("the call: %v, want CALL_TIMEOUT", err)
	}
}

func TestAFrameTooLongForThePeerIsRefusedBeforeItIsSent(t *testing.T) {
	t.Parallel()
	ccfg := clientConfig(4)
	ccfg.MaxFrameBytes = 1 << 10
	_, c, _ := pair(t, serverConfig(4), ccfg, rpc.Methods{
		"echo": rpc.HandlerFunc(echo),
		"big":  rpc.HandlerFunc(func(context.Context, corerpc.Request) (any, error) { return strings.Repeat("x", 4<<10), nil }),
	})
	big := map[string]any{"pad": strings.Repeat("y", 128<<10)}
	if err := c.Call(context.Background(), "echo", big, nil); !errs.HasCode(err, corerpc.CodeFrameTooLarge) {
		t.Fatalf("a request past the server's MaxFrameBytes: %v, want FRAME_TOO_LARGE", err)
	}
	if err := c.Call(context.Background(), "big", nil, nil); !errs.HasCode(err, corerpc.CodeFrameTooLarge) {
		t.Fatalf("a reply past the client's MaxFrameBytes: %v, want FRAME_TOO_LARGE", err)
	}
	if err := c.Call(context.Background(), "echo", map[string]any{}, nil); err != nil {
		t.Fatalf("the connection after both refusals: %v", err)
	}
}

func TestAHelloInAFormatTheServerDoesNotSpeakIsRefused(t *testing.T) {
	t.Parallel()
	scfg := serverConfig(4)
	scfg.Formats = []codec.Format{"json"}
	srv, err := rpc.NewServer(scfg, rpc.Methods{})
	if err != nil {
		t.Fatal(err)
	}
	ccfg := clientConfig(4)
	ccfg.Format = "msgpack"
	d := &pipeDialer{srv: srv, served: make(chan error, 1)}
	if _, err := rpc.NewClient(context.Background(), d, ccfg); !errs.HasCode(err, corerpc.CodeFormatRefused) {
		t.Fatalf("NewClient: %v, want FORMAT_REFUSED", err)
	}
	if err := <-d.served; !errs.HasCode(err, corerpc.CodeFormatRefused) {
		t.Fatalf("ServeConn: %v, want FORMAT_REFUSED", err)
	}
}

func TestEveryBoundIsTheCallersDecision(t *testing.T) {
	t.Parallel()
	ok := serverConfig(1)
	type tc struct {
		name string
		edit func(*rpc.ServerConfig)
	}
	cases := []tc{
		{"MaxFrameBytes zero", func(c *rpc.ServerConfig) { c.MaxFrameBytes = 0 }},
		{"MaxFrameBytes below a call", func(c *rpc.ServerConfig) { c.MaxFrameBytes = 63 }},
		{"MaxInFlight zero", func(c *rpc.ServerConfig) { c.MaxInFlight = 0 }},
		{"FrameTimeout zero", func(c *rpc.ServerConfig) { c.FrameTimeout = 0 }},
		{"no format", func(c *rpc.ServerConfig) { c.Formats = nil }},
		{"a format nobody registered", func(c *rpc.ServerConfig) { c.Formats = []codec.Format{"nope"} }},
	}
	//: only a 64-bit int can say more than a u32 length.
	if strconv.IntSize == 64 {
		cases = append(cases, tc{"MaxFrameBytes past a u32", func(c *rpc.ServerConfig) { c.MaxFrameBytes = int(^uint(0) >> 1) }})
	}
	for _, tt := range cases {
		cfg := ok
		tt.edit(&cfg)
		if _, err := rpc.NewServer(cfg, rpc.Methods{}); !errs.HasCode(err, corerpc.CodeMisconfigured) {
			t.Errorf("%s: %v, want MISCONFIGURED", tt.name, err)
		}
	}
	if _, err := rpc.NewServer(ok, nil); !errs.HasCode(err, corerpc.CodeMisconfigured) {
		t.Errorf("a nil Router: %v, want MISCONFIGURED", err)
	}
	ccfg := clientConfig(1)
	ccfg.CallTimeout = 0
	if _, err := rpc.NewClient(context.Background(), &pipeDialer{}, ccfg); !errs.HasCode(err, corerpc.CodeMisconfigured) {
		t.Errorf("CallTimeout zero: %v, want MISCONFIGURED", err)
	}
}

func TestClosingTheClientEndsItsCalls(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	_, c, d := pair(t, serverConfig(4), clientConfig(4), rpc.Methods{
		"block": rpc.HandlerFunc(func(ctx context.Context, _ corerpc.Request) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	})
	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "block", nil, nil) }()
	<-started
	closeOrLog(t, c)
	if err := <-errc; !errs.HasCode(err, corerpc.CodeConnClosed) {
		t.Fatalf("the call in flight: %v, want CONN_CLOSED", err)
	}
	if err := <-d.served; err != nil {
		t.Fatalf("ServeConn after the client hung up: %v, want nil", err)
	}
	if err := c.Call(context.Background(), "block", nil, nil); !errs.HasCode(err, corerpc.CodeConnClosed) {
		t.Fatalf("a call after Close: %v, want CONN_CLOSED", err)
	}
}

// listenerDouble is a Listener over a channel of pipe connections.
type listenerDouble struct {
	conns  chan *coreipc.Conn
	closed chan struct{}
	once   sync.Once
}

func (l *listenerDouble) Accept() (*coreipc.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, coreipc.Closed
	}
}
func (l *listenerDouble) Addr() net.Addr { return nil }
func (l *listenerDouble) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *listenerDouble) Path() string   { return "" }
func (l *listenerDouble) Refused() int64 { return 0 }

// dialerDouble dials a listenerDouble.
type dialerDouble struct{ ln *listenerDouble }

func (d dialerDouble) Dial(context.Context) (*coreipc.Conn, error) {
	client, server := net.Pipe()
	d.ln.conns <- &coreipc.Conn{Conn: server, Peer: testPeer}
	return &coreipc.Conn{Conn: client, Peer: testPeer}, nil
}

func TestServeAnswersUntilClose(t *testing.T) {
	t.Parallel()
	srv, err := rpc.NewServer(serverConfig(4), rpc.Methods{"echo": rpc.HandlerFunc(echo)})
	if err != nil {
		t.Fatal(err)
	}
	ln := &listenerDouble{conns: make(chan *coreipc.Conn), closed: make(chan struct{})}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	c, err := rpc.NewClient(context.Background(), dialerDouble{ln}, clientConfig(4))
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrLog(t, c)
	if err := c.Call(context.Background(), "echo", map[string]any{}, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	closeOrLog(t, srv)
	if err := <-served; err != nil {
		t.Fatalf("Serve after Close: %v, want nil", err)
	}
	if err := c.Call(context.Background(), "echo", map[string]any{}, nil); !errs.HasCode(err, corerpc.CodeConnClosed) {
		t.Fatalf("a call after the server closed: %v, want CONN_CLOSED", err)
	}
}
