package kit_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// The bench: a service for the dev tools' tests. It signs people up (secrets
// in and out), greets them (logs), burns CPU, hoards memory, waits on a gate,
// and says who calls it.

var Bench = kit.NewService("bench", "Dev tools under test.")

type BenchProfile struct {
	Name     string `json:"name"`
	APIKey   string `json:"apiKey"`
	Recovery string `json:"recovery" kit:"secret"`
}

type SignupInput struct {
	Email    string       `json:"email"`
	Password string       `json:"password"`
	Hint     string       `json:"hint" kit:"secret"`
	Profile  BenchProfile `json:"profile"`
	Bio      string       `json:"bio"`
	Agent    string       `header:"Authorization"`
}

type BenchAccount struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	SessionToken string `json:"sessionToken"`
	Bio          string `json:"bio"`
}

type HelloInput struct {
	Name string `query:"name"`
}

type BenchCredentials struct {
	User string `json:"user"`
	Key  string `json:"key" kit:"secret"`
}

type BurnOutput struct {
	X uint64 `json:"x"`
}

type BenchWhoOutput struct {
	User string `json:"user"`
}

var (
	_        = Bench.Endpoint("POST /signup", SignUp)
	_        = Bench.Endpoint("GET /hello", Hello)
	_        = Bench.Endpoint("GET /burn", Burn)
	_        = Bench.Endpoint("POST /hoard", Hoard)
	_        = Bench.Endpoint("GET /wait", Wait)
	BenchWho = Bench.Query("who", WhoCalls)
	_        = Bench.Loop("tick", Tick, kit.WakeEvery(time.Hour))
	_        = Bench.Go("reaper", Reap)
)

// SignUp echoes the account, with a session token.
func SignUp(_ context.Context, in SignupInput) (BenchAccount, error) {
	return BenchAccount{ID: "acct_1", Email: in.Email, SessionToken: "tok-sensitive-7d1", Bio: in.Bio}, nil
}

// Hello logs at every level, secrets included.
func Hello(ctx context.Context, in HelloInput) (kit.EmptyValue, error) {
	lg := kit.Log(ctx)
	logger.Debug(ctx, lg, "counting sheep", logger.Int("sheep", 3))
	logger.Info(ctx, lg, "hello",
		logger.String("user", in.Name),
		logger.String("password", "hunter2"),
		logger.Any("creds", BenchCredentials{User: "ann", Key: "k-123"}),
		logger.String("dsn", "postgres://ann:pw@db:5432/x"),
		logger.Duration("took", 1500*time.Millisecond))
	logger.Warn(ctx, lg, "careful")
	return kit.EmptyValue{}, nil
}

// Burn spins the CPU for a while.
func Burn(_ context.Context, _ kit.EmptyValue) (BurnOutput, error) {
	x := uint64(1)
	for deadline := time.Now().Add(20 * time.Millisecond); time.Now().Before(deadline); {
		for range 2000 {
			x = x*6364136223846793005 + 1442695040888963407
		}
	}
	return BurnOutput{X: x}, nil
}

// hoard keeps what Hoard allocates.
var hoard struct {
	sync.Mutex
	kept [][]byte
}

// Hoard keeps 8 MiB for good.
func Hoard(_ context.Context, _ kit.EmptyValue) (kit.EmptyValue, error) {
	hoard.Lock()
	defer hoard.Unlock()
	for range 8 {
		hoard.kept = append(hoard.kept, make([]byte, 1<<20))
	}
	return kit.EmptyValue{}, nil
}

// gate holds Wait until a test opens it.
var gate = struct {
	sync.Mutex
	ch chan struct{}
}{ch: make(chan struct{})}

func gateCh() chan struct{} {
	gate.Lock()
	defer gate.Unlock()
	return gate.ch
}

func openGate() {
	gate.Lock()
	defer gate.Unlock()
	close(gate.ch)
	gate.ch = make(chan struct{})
}

// Wait blocks until the gate opens.
func Wait(ctx context.Context, _ kit.EmptyValue) (kit.EmptyValue, error) {
	select {
	case <-gateCh():
	case <-ctx.Done():
	}
	return kit.EmptyValue{}, nil
}

// WhoCalls says who calls.
func WhoCalls(ctx context.Context, _ kit.EmptyValue) (BenchWhoOutput, error) {
	uid, _ := kit.UserID(ctx)
	return BenchWhoOutput{User: string(uid)}, nil
}

// Tick is a declared loop that does nothing.
func Tick(context.Context, kit.WakeEvent) error { return nil }

// Reap is a hand-written loop that waits for the end.
func Reap(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// startBench runs the bench beside the shop, in dev, in memory.
func startBench(t *testing.T, opts ...kit.AppConfigurer) *kit.App {
	t.Helper()
	app := kit.NewApp("bench", Bench, Shop, Audit).With(append([]kit.AppConfigurer{
		kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard),
	}, opts...)...)
	if err := app.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Stop(ctx); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	return app
}

// subscribe opens the live event stream, past its hello.
//
// Goroutine lifecycle: one goroutine reads the event stream into the channel
// and closes it when the stream ends — the test's context ends the request.
func subscribe(t *testing.T, app *kit.App) <-chan model.Event {
	t.Helper()
	req, reqErr := http.NewRequestWithContext(t.Context(), "GET", app.URL()+"/_kit/api/events", nil)
	if reqErr != nil {
		t.Fatal(reqErr)
	}
	resp, respErr := http.DefaultClient.Do(req)
	if respErr != nil {
		t.Fatal(respErr)
	}
	ch := make(chan model.Event, 4096)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		dec := &sseReader{r: resp.Body}
		for {
			raw, err := dec.next()
			if err != nil {
				return
			}
			var e model.Event
			if json.Unmarshal(raw, &e) == nil {
				ch <- e
			}
		}
	}()
	select {
	case e := <-ch:
		if e.Type != model.EventHello {
			t.Fatalf("the stream opened with %s", e.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	return ch
}

// awaitEvent reads the stream until an event matches.
func awaitEvent(t *testing.T, ch <-chan model.Event, what string, match func(model.Event) bool) model.Event {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("the stream closed before %s", what)
			}
			if match(e) {
				return e
			}
		case <-timeout:
			t.Fatalf("no event: %s", what)
		}
	}
}

// traceOf returns the trace a response names in its traceparent header.
func traceOf(t *testing.T, app *kit.App, r response) model.Trace {
	t.Helper()
	parts := strings.Split(r.header.Get("traceparent"), "-")
	if len(parts) != 4 {
		t.Fatalf("no traceparent: %v", r.header)
	}
	var tr model.Trace
	eventually(t, "the trace", func() bool {
		got := call(t, app, "GET /_kit/api/traces/"+parts[1], noBody)
		if got.status != http.StatusOK {
			return false
		}
		got.json(t, &tr)
		return true
	})
	return tr
}

// spanOf returns the span of node in tr.
func spanOf(t *testing.T, tr model.Trace, node string) model.Span {
	t.Helper()
	for _, s := range tr.Spans {
		if s.Node == node {
			return s
		}
	}
	t.Fatalf("no span on %s in %+v", node, tr.Spans)
	return model.Span{}
}

// loopOf returns the loop called name in the app's graph.
func loopOf(t *testing.T, app *kit.App, name string) model.Loop {
	t.Helper()
	g := app.Graph()
	if g.Runtime == nil {
		t.Fatal("no runtime")
	}
	for _, l := range g.Runtime.Loops {
		if l.Name == name {
			return l
		}
	}
	t.Fatalf("no loop %q in %+v", name, g.Runtime.Loops)
	return model.Loop{}
}
