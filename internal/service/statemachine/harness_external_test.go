// Package statemachine_test — the harness every suite of the package shares:
// an in-memory store that tells the machine about every write, as a document
// store with write hooks does, a journal that records its calls, and the
// entity the suites move around.
package statemachine_test

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// The states of an item.
const (
	Draft State = iota + 1
	Live
	Sold
	Expired
	Retired
)

// State is an item's state.
type State uint8

// stateNames spells the states.
var stateNames = [...]string{"", "draft", "live", "sold", "expired", "retired"}

// String names a state.
func (s State) String() string {
	if int(s) >= len(stateNames) {
		return "?"
	}
	return stateNames[s]
}

// Item is the entity the suites drive.
type Item struct {
	Expires *time.Time `json:"expires,omitempty"`
	ID      string     `json:"id"`
	State   State      `json:"state"`
	Note    string     `json:"note,omitempty"`
	Stock   int        `json:"stock"`
}

// stateOf is the accessor every definition uses.
func stateOf(i *Item) *State { return &i.State }

// memStore is a Store over a map, encoding each item as JSON so what a caller
// holds never aliases what the store holds. When notify is set it tells the
// machine about every write and delete, the way a document store with write
// hooks does — the machine's own writes included.
type memStore struct {
	notify    func(ctx context.Context, key string, deleted bool)
	afterGet  func(ctx context.Context, key string)
	failNext  error
	data      map[string][]byte
	mu        sync.Mutex
	panicNext bool
}

// newMemStore returns an empty store.
func newMemStore() *memStore { return &memStore{data: make(map[string][]byte)} }

// Key is the item's ID.
func (s *memStore) Key(i Item) string { return i.ID }

// Get decodes the item under key. A hook set by onNextGet runs once the item
// is read and before it is returned, as a write racing the read would.
func (s *memStore) Get(ctx context.Context, key string) (Item, bool, error) {
	raw, ok, err := s.lookup(key)
	if hook := s.takeAfterGet(); hook != nil {
		hook(ctx, key)
	}
	if err != nil || !ok {
		return Item{}, false, err
	}
	var i Item
	if err := json.Unmarshal(raw, &i); err != nil {
		return Item{}, false, err
	}
	return i, true, nil
}

// lookup returns the raw item under key, or the failure injected.
func (s *memStore) lookup(key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.panicNext {
		s.panicNext = false
		panic("a store's bug")
	}
	if err := s.failNext; err != nil {
		s.failNext = nil
		return nil, false, err
	}
	raw, ok := s.data[key]
	return raw, ok, nil
}

// onNextGet sets a hook the next Get runs once it has read the item.
func (s *memStore) onNextGet(hook func(ctx context.Context, key string)) {
	s.mu.Lock()
	s.afterGet = hook
	s.mu.Unlock()
}

// takeAfterGet returns the hook for this Get, and clears it.
func (s *memStore) takeAfterGet() func(ctx context.Context, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hook := s.afterGet
	s.afterGet = nil
	return hook
}

// panicOnce makes the next read panic.
func (s *memStore) panicOnce() {
	s.mu.Lock()
	s.panicNext = true
	s.mu.Unlock()
}

// Insert stores a new item.
func (s *memStore) Insert(ctx context.Context, i Item) (bool, error) {
	return s.write(ctx, i, false)
}

// Replace stores over an existing item.
func (s *memStore) Replace(ctx context.Context, i Item) (bool, error) {
	return s.write(ctx, i, true)
}

// write stores i when its key's presence is what the call expects.
func (s *memStore) write(ctx context.Context, i Item, existing bool) (bool, error) {
	raw, err := json.Marshal(i)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	if err := s.failNext; err != nil {
		s.failNext = nil
		s.mu.Unlock()
		return false, err
	}
	if _, ok := s.data[i.ID]; ok != existing {
		s.mu.Unlock()
		return false, nil
	}
	s.data[i.ID] = raw
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(ctx, i.ID, false)
	}
	return true, nil
}

// All yields every item, ordered by key.
func (s *memStore) All(ctx context.Context) iter.Seq2[Item, error] {
	return func(yield func(Item, error) bool) {
		s.mu.Lock()
		keys := slices.Sorted(maps.Keys(s.data))
		s.mu.Unlock()
		for _, key := range keys {
			i, ok, err := s.Get(ctx, key)
			if !ok && err == nil {
				continue
			}
			if !yield(i, err) || err != nil {
				return
			}
		}
	}
}

// put writes an item directly, as code that bypasses the machine does, and
// notifies like any other write.
func (s *memStore) put(t *testing.T, i Item) {
	t.Helper()
	raw, err := json.Marshal(i)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.data[i.ID] = raw
	notify := s.notify
	s.mu.Unlock()
	if notify != nil {
		notify(t.Context(), i.ID, false)
	}
}

// remove deletes an item and notifies.
func (s *memStore) remove(ctx context.Context, key string) bool {
	s.mu.Lock()
	_, ok := s.data[key]
	delete(s.data, key)
	notify := s.notify
	s.mu.Unlock()
	if ok && notify != nil {
		notify(ctx, key, true)
	}
	return ok
}

// read returns the stored item, failing the test when there is none.
func (s *memStore) read(t *testing.T, key string) Item {
	t.Helper()
	i, ok, err := s.Get(t.Context(), key)
	if err != nil || !ok {
		t.Fatalf("store read %q: %v, found %v", key, err, ok)
	}
	return i
}

// failWith makes the next Get, Insert or Replace fail with err.
func (s *memStore) failWith(err error) {
	s.mu.Lock()
	s.failNext = err
	s.mu.Unlock()
}

// wire makes the store tell m about every write and delete, the machine's
// own included; a notification that fails fails the test.
func wire(t *testing.T, s *memStore, m *svcstm.StateMachine[Item, State]) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notify = func(ctx context.Context, key string, deleted bool) {
		tell := m.Changed
		if deleted {
			tell = m.Deleted
		}
		if err := tell(ctx, key); err != nil {
			t.Errorf("notifying %q: %v", key, err)
		}
	}
}

// memJournal is a Journal over a map that counts its calls.
type memJournal struct {
	failSave error
	failLoad error
	records  map[string]corestm.RecordValue[State]
	saves    int
	deletes  int
	mu       sync.Mutex
}

// newMemJournal returns an empty journal.
func newMemJournal() *memJournal {
	return &memJournal{records: make(map[string]corestm.RecordValue[State])}
}

// Load returns every record, through JSON, as a file journal would.
func (j *memJournal) Load(context.Context) ([]corestm.RecordValue[State], error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failLoad != nil {
		return nil, j.failLoad
	}
	raw, err := json.Marshal(slices.Collect(maps.Values(j.records)))
	if err != nil {
		return nil, err
	}
	var out []corestm.RecordValue[State]
	return out, json.Unmarshal(raw, &out)
}

// Save stores records.
func (j *memJournal) Save(_ context.Context, records ...corestm.RecordValue[State]) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.saves++
	if j.failSave != nil {
		return j.failSave
	}
	for _, rec := range records {
		j.records[rec.Key] = rec
	}
	return nil
}

// Delete forgets records.
func (j *memJournal) Delete(_ context.Context, keys ...string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.deletes++
	for _, key := range keys {
		delete(j.records, key)
	}
	return nil
}

// record returns the journal's record of key.
func (j *memJournal) record(key string) (corestm.RecordValue[State], bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.records[key]
	return rec, ok
}

// hookedJournal is a memJournal whose Save and Delete call before first, with
// the operation and the keys: a journal that reads the machine, or one that
// takes its time.
type hookedJournal struct {
	*memJournal
	before func(op string, keys []string)
}

// Save calls before, then stores records.
func (j *hookedJournal) Save(ctx context.Context, records ...corestm.RecordValue[State]) error {
	keys := make([]string, len(records))
	for i, rec := range records {
		keys[i] = rec.Key
	}
	j.before("save", keys)
	return j.memJournal.Save(ctx, records...)
}

// Delete calls before, then forgets records.
func (j *hookedJournal) Delete(ctx context.Context, keys ...string) error {
	j.before("delete", keys)
	return j.memJournal.Delete(ctx, keys...)
}

// reports collects what Config.Report receives.
type reports struct {
	errs []error
	mu   sync.Mutex
}

// add is the Report hook.
func (r *reports) add(_ context.Context, err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

// all returns what was reported so far.
func (r *reports) all() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.errs)
}

// start is the instant every manual clock of the suites starts at.
var start = time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)

// open builds a machine over store with cfg, failing the test on an error.
func open(t *testing.T, def *svcstm.MachineSpec[Item, State], cfg *svcstm.Config[Item, State]) *svcstm.StateMachine[Item, State] {
	t.Helper()
	m, err := svcstm.NewStateMachine(t.Context(), def, cfg)
	if err != nil {
		t.Fatalf("NewStateMachine: %v", err)
	}
	return m
}

// running runs m's loop on a background goroutine until the test ends: the
// cleanup cancels its context and waits for Run to return.
func running(t *testing.T, m *svcstm.StateMachine[Item, State]) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run returned %v after cancellation", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
}

// loopEvents returns an OnLoop hook feeding a buffered channel.
func loopEvents() (func(svcstm.LoopEvent), chan svcstm.LoopEvent) {
	ch := make(chan svcstm.LoopEvent, 1024)
	return func(e svcstm.LoopEvent) {
		select {
		case ch <- e:
		default:
		}
	}, ch
}

// advancer is the part of a manual clock the helpers move.
type advancer interface {
	Advance(d time.Duration)
}

// awaitRun waits for a run's end matching pred, advancing clk a second at a
// time so a paced loop gets to run.
func awaitRun(t *testing.T, clk advancer, events chan svcstm.LoopEvent, what string, pred func(svcstm.LoopEvent) bool) svcstm.LoopEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatal("the loop events were closed")
			}
			if e.Kind == svcstm.LoopRunEnded && pred(e) {
				return e
			}
		case <-time.After(5 * time.Millisecond):
			clk.Advance(time.Second)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// eventually polls cond, advancing clk when there is one, until it holds.
func eventually(t *testing.T, clk advancer, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		if clk != nil {
			clk.Advance(time.Second)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// within runs call on a background goroutine and fails the test when it has
// not returned in ten seconds — a deadlock — returning its error otherwise.
func within(t *testing.T, what string, call func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- call() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never returned", what)
		return nil
	}
}

// errBoom is a failure the suites inject.
var errBoom = errors.New("boom")
