package app

import (
	"context"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	sdkstream "github.com/kitsunium/sdk/pkg/v1/data/stream"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// countingStream counts the appends made to the SDK's stream it stands in
// front of: one Append is one batch under one sync on the file engine.
type countingStream struct {
	sdkstream.Stream
	appends *atomic.Int64
}

// Append counts, then appends.
func (c *countingStream) Append(ctx context.Context, records []sdkstream.Record) (sdkstream.Appended, error) {
	c.appends.Add(1)
	return c.Stream.Append(ctx, records)
}

// The siblings the subsystem asks for, forwarded.
func (c *countingStream) Committed(ctx context.Context, group string, partition int) (sdkstream.Offset, error) {
	return c.Stream.(sdkstream.Committer).Committed(ctx, group, partition)
}

// Commit forwards.
func (c *countingStream) Commit(ctx context.Context, group string, partition int, next sdkstream.Offset) error {
	return c.Stream.(sdkstream.Committer).Commit(ctx, group, partition, next)
}

// Wake forwards, so an idle group wakes on the next append.
func (c *countingStream) Wake() <-chan struct{} { return c.Stream.(sdkstream.Waker).Wake() }

// Close forwards, so the directory is released at the app's stop.
func (c *countingStream) Close() error {
	if cl, ok := c.Stream.(io.Closer); ok {
		return cl.Close()
	}
	return nil
}

// heard is what a subscription's handler received, in order.
type heard struct {
	mu   sync.Mutex
	keys []string
}

// handler records each message's key.
func (h *heard) handler(_ context.Context, o probeOrder) error {
	h.mu.Lock()
	h.keys = append(h.keys, o.ID)
	h.mu.Unlock()
	return nil
}

// got is what it received so far, sorted.
func (h *heard) got() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := slices.Clone(h.keys)
	slices.Sort(out)
	return out
}

// streamedApp runs, in dir on the file engine, a service whose topic is
// streamed with a subscription per name of subs, each started where opts
// say, and counts the appends to its stream.
func streamedApp(t *testing.T, dir string, appends *atomic.Int64, subs map[string]*heard, opts ...SubscriptionConfigurer) (*App, *TopicService[probeOrder]) {
	t.Helper()
	svc := NewService("orders", "Orders.")
	topic := svc.Topic[probeOrder]("placed").Streamed(func(o probeOrder) string { return o.ID })
	names := make([]string, 0, len(subs))
	for name := range subs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		svc.Subscribe(name, topic, subs[name].handler, opts...)
	}
	app := NewApp("orders", svc).With(Profile("daemon"), DataDir(dir), Logs(io.Discard))
	app.wrapStream = func(s sdkstream.Stream) sdkstream.Stream { return &countingStream{Stream: s, appends: appends} }
	startStreamed(t, app)
	return app, topic
}

// A streamed topic with three subscriptions on the file engine appends each
// publish ONCE — one batch, one sync — where a queue's topic writes one
// copy per subscription; every subscription still hears every message. A
// subscription added later with FromBeginning reads every earlier message,
// and one added FromNow none of them.
func TestAStreamedTopicAppendsOncePerPublish(t *testing.T) {
	dir := t.TempDir()
	var appends atomic.Int64
	subs := map[string]*heard{"bill": {}, "ship": {}, "audit": {}}
	app, topic := streamedApp(t, dir, &appends, subs)
	keys := []string{"o1", "o2", "o3", "o4"}
	for _, k := range keys {
		must(t, topic.Publish(t.Context(), probeOrder{ID: k}))
	}
	if n := appends.Load(); n != int64(len(keys)) {
		t.Errorf("%d publishes to a streamed topic of three subscriptions made %d appends, want one each", len(keys), n)
	}
	for name, h := range subs {
		eventuallyInternal(t, name+" to hear every message", func() bool { return slices.Equal(h.got(), keys) })
	}
	must(t, app.Stop(context.Background()))

	late := map[string]*heard{"late": {}}
	again, _ := streamedApp(t, dir, &appends, late, FromBeginning())
	eventuallyInternal(t, "a subscription added FromBeginning to read every earlier message", func() bool {
		return slices.Equal(late["late"].got(), keys)
	})
	must(t, again.Stop(context.Background()))

	now := map[string]*heard{"now": {}}
	third, topic3 := streamedApp(t, dir, &appends, now, FromNow())
	must(t, topic3.Publish(t.Context(), probeOrder{ID: "o5"}))
	eventuallyInternal(t, "a subscription added FromNow to hear what is published after it", func() bool {
		return slices.Equal(now["now"].got(), []string{"o5"})
	})
	must(t, third.Stop(context.Background()))
}

// A publish to a streamed topic inside a transaction is appended at the
// commit, and a rollback appends nothing.
func TestAStreamedPublishWaitsForTheCommit(t *testing.T) {
	var appends atomic.Int64
	subs := map[string]*heard{"bill": {}}
	app, topic := streamedApp(t, t.TempDir(), &appends, subs)
	t.Cleanup(func() { _ = app.Stop(context.Background()) })
	refused := errors.New("rolled back")
	err := Transact(app.baseCtx, func(ctx context.Context) error {
		must(t, topic.Publish(ctx, probeOrder{ID: "gone"}))
		return refused
	})
	if !errors.Is(err, refused) {
		t.Fatalf("the transaction: %v", err)
	}
	if n := appends.Load(); n != 0 {
		t.Errorf("a rolled-back publish made %d appends", n)
	}
	must(t, Transact(app.baseCtx, func(ctx context.Context) error { return topic.Publish(ctx, probeOrder{ID: "kept"}) }))
	if n := appends.Load(); n != 1 {
		t.Errorf("a committed publish made %d appends, want 1", n)
	}
	eventuallyInternal(t, "the committed message to be heard", func() bool { return slices.Equal(subs["bill"].got(), []string{"kept"}) })
}

// withoutStreams runs the test as a program that does not import
// framework/app/stream: the hook its package sets is nil.
func withoutStreams(t *testing.T) {
	t.Helper()
	saved := plug.Streams.Load()
	plug.Streams.Store(nil)
	t.Cleanup(func() { plug.Streams.Store(saved) })
}

// streamedProduct declares a streamed topic and a subscription to it.
func streamedProduct(t *testing.T) *App {
	svc := NewService("post", "Posts.")
	letters := svc.Topic[probeOrder]("letters").Streamed(nil)
	svc.Subscribe("read", letters, func(context.Context, probeOrder) error { return nil })
	return NewApp("post", svc).With(Profile("daemon"), DataDir(t.TempDir()), Logs(io.Discard))
}

// startStreamed starts app, whose streamed topics keep their records in the
// stream file engine under its data directory, and skips t where that engine
// is refused by design, and only there. The engine keeps its groups' cursors
// in vfs's disk filesystem, which Windows, illumos and Solaris do not have
// (ADR 0018); the predicate is vfs.NewOS's own refusal, never a list of GOOS.
// Where vfs refuses, the start must fail with that very refusal, asserted
// before the skip (ADR 0095); anywhere else it must succeed.
func startStreamed(t *testing.T, app *App) {
	t.Helper()
	disk, vfsErr := vfs.NewOS(t.TempDir())
	if closer, ok := disk.(io.Closer); ok {
		must(t, closer.Close())
	}
	err := app.Start(t.Context())
	if vfsErr == nil {
		must(t, err)
		return
	}
	if !errors.Is(err, vfsErr) {
		t.Fatalf("Start on %s = %v, want the file engine's refusal, %v", runtime.GOOS, err, vfsErr)
	}
	t.Skipf("a streamed topic keeps its records in the stream file engine, whose cursors need vfs's disk filesystem, which %s refuses by design (%v); the start refused the same way, asserted above (ADR 0095)", runtime.GOOS, vfsErr)
}

// A product that declares a streamed topic without importing
// framework/app/stream is refused at the start: the topic and its
// subscription, each naming the package, with CodeSubsystemMissing. It needs
// no queue: without framework/app/queue it starts.
func TestAStreamedTopicWithoutItsSubsystemIsRefused(t *testing.T) {
	t.Run("without the streams", func(t *testing.T) {
		withoutStreams(t)
		err := streamedProduct(t).Start(context.Background())
		var de *DiagnosticsError
		if !errors.As(err, &de) {
			t.Fatalf("the start: %v", err)
		}
		var refused []string
		for _, d := range de.Diagnostics {
			if strings.Contains(d.Message, streamPackage) {
				refused = append(refused, d.Node)
			}
		}
		slices.Sort(refused)
		if want := []string{"post/subscription/read", "post/topic/letters"}; !slices.Equal(refused, want) {
			t.Errorf("refused for want of %s: %q, want %q\n%v", streamPackage, refused, want, de)
		}
		if !slices.Contains(de.Missing, streamPackage) || !errs.HasCode(err, CodeSubsystemMissing) {
			t.Errorf("the refusal does not name %s with CodeSubsystemMissing: %v", streamPackage, err)
		}
	})
	t.Run("without the queues", func(t *testing.T) {
		withoutQueues(t)
		app := streamedProduct(t)
		startStreamed(t, app)
		must(t, app.Stop(context.Background()))
	})
}

// A streamed topic's subscription refuses a queue's options by name, and a
// queue's subscription refuses where to start.
func TestAStreamedSubscriptionRefusesAQueuesOptions(t *testing.T) {
	svc := NewService("post", "Posts.")
	streamed := svc.Topic[probeOrder]("letters").Streamed(nil)
	queued := svc.Topic[probeOrder]("notes")
	svc.Subscribe("leased", streamed, func(context.Context, probeOrder) error { return nil }, Lease(0))
	svc.Subscribe("started", queued, func(context.Context, probeOrder) error { return nil }, FromBeginning())
	err := NewApp("post", svc).With(Profile("daemon"), InMemory(), Logs(io.Discard)).Start(context.Background())
	var de *DiagnosticsError
	if !errors.As(err, &de) {
		t.Fatalf("the start: %v", err)
	}
	said := de.Error()
	for _, want := range []string{`"leased" is given Lease`, `"started" is given where to start`} {
		if !strings.Contains(said, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, said)
		}
	}
}
