package events_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	corev "github.com/kitsunium/sdk/internal/core/events"
	svcev "github.com/kitsunium/sdk/internal/service/events"
)

// orderPlaced is the domain event every test publishes. A struct rather than
// a named string so the routing key is a real type the compiler owns.
type orderPlaced struct {
	// id distinguishes two published events in a recorder's trace.
	id int
}

// orderShipped is a SECOND event type, used to prove a bus keyed on Go types
// never crosses them.
type orderShipped struct {
	// id distinguishes two published events in a recorder's trace.
	id int
}

// errDisk stands in for whatever a listener's own failure is. It is a stdlib
// error on purpose: the whole point of the errors.Join aggregate is that a
// caller's errors.Is against THEIR sentinel keeps working.
var errDisk = errors.New("disk full")

// recorder collects the listener names that ran, in the order they ran, so an
// ordering promise is asserted on the observable trace rather than on an
// internal field.
type recorder struct {
	// mu guards calls; a dispatch is single-goroutine but the concurrency
	// tests publish from several.
	mu sync.Mutex
	// calls is the trace, in call order.
	calls []string
}

// newRecorder returns an empty recorder.
func newRecorder() *recorder { return &recorder{} }

// note appends one call to the trace.
func (r *recorder) note(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name)
}

// snapshot copies the trace so the caller can assert without holding a lock.
func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// handler builds a Handler that records its name and returns err.
func (r *recorder) handler(name string, priority corev.Priority, err error) svcev.Handler[orderPlaced] {
	return svcev.Handler[orderPlaced]{
		Name:     name,
		Priority: priority,
		Handle: func(_ context.Context, _ orderPlaced) error {
			r.note(name)
			return err
		},
	}
}

// on registers every handler, failing the test on the first refusal.
func on(t *testing.T, bus corev.Bus, handlers ...svcev.Handler[orderPlaced]) {
	t.Helper()
	for _, handler := range handlers {
		if err := svcev.On(bus, handler); err != nil {
			t.Fatalf("On(%q): %v", handler.Name, err)
		}
	}
}

// publish publishes one orderPlaced, failing the test on an unexpected error.
func publish(t *testing.T, bus corev.Bus, event orderPlaced) corev.DispatchValue {
	t.Helper()
	report, err := bus.Publish(context.Background(), event)
	if err != nil {
		t.Fatalf("Publish: unexpected error: %v", err)
	}
	return report
}

// assertCalls compares a trace against the expected order.
func assertCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("call order: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call order: got %v, want %v", got, want)
		}
	}
}
