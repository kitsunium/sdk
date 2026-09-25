// Package secret — the in-process store.
package secret

import (
	"context"
	"maps"
	"slices"
	"sync"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// initialSecrets is the map's starting capacity: a service holds a handful of
// secrets, not thousands, so this avoids the first growth steps and no more.
const initialSecrets int = 8

// MemoryConfig parameterises [NewMemory]. Its zero value is a working store.
type MemoryConfig struct {
	// Clock stamps each version's Created. nil means clock.System; a test
	// driving a Rotator passes the same ManualClock to both, so the store's
	// stamps and the rotator's "now" are one timeline.
	Clock clock.Clock
}

// memoryStore keeps every version in one map guarded by an RWMutex. It dies
// with the process, which is the whole of its contract: it is the store for a
// test, a development run, or a secret that is regenerated on every start and
// never needs to outlive it.
type memoryStore struct {
	// mu guards secrets. Reads take the read lock; Put and Prune the write
	// lock, so a Put's read-then-append is one step.
	mu sync.RWMutex
	// secrets maps a name to its kept versions, newest first.
	secrets map[string][]coresecret.VersionValue
	// clk stamps Created.
	clk clock.Clock
}

// NewMemory returns a Store that keeps every version in this process's memory.
// It cannot fail: there is nothing to open and nothing a configuration can get
// wrong.
func NewMemory(cfg MemoryConfig) coresecret.Store {
	//: a nil clock is a working configuration, so it is filled, not refused.
	clk := cfg.Clock
	//: the production default.
	if clk == nil {
		//: the shared, concurrency-safe system reading.
		clk = clock.System
	}
	//: an empty store.
	return &memoryStore{secrets: make(map[string][]coresecret.VersionValue, initialSecrets), clk: clk}
}

// Get returns the newest version of name.
//
// The context is unused and named so: this store never blocks and never
// performs I/O, so there is nothing a cancellation could interrupt, and
// honouring it would mean inventing a failure the store cannot have.
func (m *memoryStore) Get(_ context.Context, name string) (current coresecret.VersionValue, err error) {
	//: a malformed name is refused before any lookup.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return coresecret.VersionValue{}, nameErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	versions := m.secrets[name]
	//: a name with no version is NotFound, not a zero version.
	if len(versions) == 0 {
		//: NotFound, naming the secret.
		return coresecret.VersionValue{}, notFound(name)
	}
	//: the head is the newest; a VersionValue is a value, so this is a copy.
	return versions[0], nil
}

// Versions returns every kept version of name, newest first.
func (m *memoryStore) Versions(_ context.Context, name string) (versions []coresecret.VersionValue, err error) {
	//: a malformed name is refused before any lookup.
	if nameErr := coresecret.ValidateName(name); nameErr != nil {
		//: InvalidName.
		return nil, nameErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	kept := m.secrets[name]
	//: a name with no version is NotFound, never an empty history.
	if len(kept) == 0 {
		//: NotFound.
		return nil, notFound(name)
	}
	//: a copy, so the caller's slice is not the store's.
	return slices.Clone(kept), nil
}

// Put stores value as the next version of name.
func (m *memoryStore) Put(_ context.Context, name string, value coresecret.Value) (created coresecret.VersionValue, err error) {
	//: the name and the value, checked as every store checks them.
	if putErr := checkPut(name, value.IsZero()); putErr != nil {
		//: InvalidName or EmptyValue.
		return coresecret.VersionValue{}, putErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.secrets[name]
	created = coresecret.VersionValue{
		Name:    name,
		Version: nextVersion(kept),
		Value:   value,
		Created: m.clk.Now(),
	}
	//: the new version goes to the head, preserving newest-first. The slice
	//: is rebuilt rather than grown in place, so a copy handed out earlier by
	//: Versions can never alias it.
	m.secrets[name] = append([]coresecret.VersionValue{created}, kept...)
	//: the version as stored.
	return created, nil
}

// Prune keeps only the newest keep versions of name.
func (m *memoryStore) Prune(_ context.Context, name string, keep int) error {
	//: the name and the bound, checked as every store checks them.
	if pruneErr := checkPrune(name, keep); pruneErr != nil {
		//: InvalidName or InvalidKeep.
		return pruneErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.secrets[name]
	//: pruning a secret that does not exist is not a no-op: the caller named
	//: something that is not there, which is worth saying.
	if len(kept) == 0 {
		//: NotFound.
		return notFound(name)
	}
	m.secrets[name] = pruned(kept, keep)
	//: pruned.
	return nil
}

// Names lists every name holding a version, sorted.
func (m *memoryStore) Names(_ context.Context) (names []string, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	//: every name in the map holds at least one version, since Prune keeps
	//: one; sorted, so two calls on one store answer identically.
	return slices.Sorted(maps.Keys(m.secrets)), nil
}
