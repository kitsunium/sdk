package kit_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// runKeyed dispatches the keyed lab command once per key, at once, holding
// every run until each could start, and says how many ran at once.
func runKeyed(t *testing.T, keys ...string) int32 {
	t.Helper()
	labKeyed.hold()
	var wg sync.WaitGroup
	for _, k := range keys {
		wg.Go(func() {
			if _, err := LabKeyed.Dispatch(t.Context(), LabInput{Key: k}); err != nil {
				t.Errorf("key %s: %v", k, err)
			}
		})
	}
	eventually(t, "a run holding its key", func() bool { return labKeyed.running.Load() >= 1 })
	time.Sleep(20 * time.Millisecond)
	labKeyed.release()
	wg.Wait()
	return labKeyed.most.Load()
}

// Two runs of a command with one key never overlap; with two keys they run
// at once. A key that names nothing is refused.
func TestAKeyRunsOneAtATime(t *testing.T) {
	startCounter(t)
	if most := runKeyed(t, "order_1", "order_1"); most != 1 {
		t.Errorf("two runs of one key overlapped: %d at once", most)
	}
	if most := runKeyed(t, "order_1", "order_2"); most != 2 {
		t.Errorf("two keys did not run at once: %d at once", most)
	}
	_, err := LabKeyed.Dispatch(t.Context(), LabInput{})
	expectInvalid(t, err, "an input that names no key")
}

// A handler dispatching its own command with its own key is refused, never a
// deadlock — which a product reaches through a port, or a command declared
// in a function: a package-level handler naming its own command is an
// initialization cycle.
func TestAKeyIsNotTakenTwiceByOneRun(t *testing.T) {
	again := kit.NewService("again", "A command that dispatches itself.")
	var self *kit.Command[LabInput, kit.EmptyValue]
	self = again.Command("self", func(ctx context.Context, in LabInput) (kit.EmptyValue, error) {
		if in.Note == "again" {
			return self.Dispatch(ctx, LabInput{Key: in.Key})
		}
		return kit.EmptyValue{}, nil
	}).Key(func(in LabInput) string { return in.Key })
	startApp(t, []*kit.Service{Staff, Counter, Lab, again})
	_, err := self.Dispatch(t.Context(), LabInput{Key: "k", Note: "again"})
	if !errs.HasCode(err, kit.CodeCommandReentrant) {
		t.Errorf("a handler dispatching its own command with its own key: %v", err)
	}
	if _, err := self.Dispatch(t.Context(), LabInput{Key: "k"}); err != nil {
		t.Errorf("the key is held no longer: %v", err)
	}
}

// A second run waits for the key within its caller's deadline, and says so.
//
// Goroutine lifecycle: one goroutine holds the key with the first run and
// reports on a buffered channel; the deferred release ends it, and the test
// waits for it.
func TestAKeyIsWaitedForWithinTheDeadline(t *testing.T) {
	startCounter(t)
	labKeyed.hold()
	first := make(chan error, 1)
	go func() {
		_, err := LabKeyed.Dispatch(context.WithoutCancel(t.Context()), LabInput{Key: "busy"})
		first <- err
	}()
	defer func() {
		labKeyed.release()
		if err := <-first; err != nil {
			t.Errorf("the first run: %v", err)
		}
	}()
	eventually(t, "the first run", func() bool { return labKeyed.running.Load() == 1 })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := LabKeyed.Dispatch(ctx, LabInput{Key: "busy"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("the second run, its deadline passed: %v", err)
	}
}

// The key is the span's instance: the Studio filters the runs of one
// entity.
func TestAKeyIsItsSpansInstance(t *testing.T) {
	app := startCounter(t)
	placed := place(t, "alice", "tea")
	r := call(t, app, "POST /orders/"+placed.ID+"/cancel", noBody, badge("alice")...)
	if s := spanOf(t, traceOf(t, app, r), "counter/command/cancel-order"); s.Attrs["instance"] != placed.ID {
		t.Errorf("the dispatch's span: %+v", s)
	}
}
