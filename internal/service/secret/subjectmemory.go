// Package secret — the in-process subject-key store.
package secret

import (
	"bytes"
	"context"
	"iter"
	"maps"
	"slices"
	"sync"

	coresecret "github.com/kitsunium/sdk/internal/core/secret"
)

// memorySubjectKeys keeps every wrapped key in one map guarded by an RWMutex.
// It dies with the process, which is its whole contract: the store for a test
// or a development run, where the data the keys protect dies with the process
// too. A deployment that persists sealed data persists its keys, or the next
// start opens nothing.
type memorySubjectKeys struct {
	// mu guards keys. Insert, Replace and Delete take the write lock, so each
	// compare and its write are one step.
	mu sync.RWMutex
	// keys maps a subject to its wrapped key; every slice is the store's own.
	keys map[string][]byte
}

// NewMemorySubjectKeyStore returns a core/secret.SubjectKeyStore that keeps
// every wrapped key in this process's memory. It cannot fail. Insert and
// Replace are atomic within the process — the only one that can see the map.
func NewMemorySubjectKeyStore() coresecret.SubjectKeyStore {
	//: an empty store.
	return &memorySubjectKeys{keys: make(map[string][]byte, initialSecrets)}
}

// Get returns the wrapped key filed under subject.
//
// The context is unused and named so: this store never blocks and never
// performs I/O, so there is nothing a cancellation could interrupt.
func (m *memorySubjectKeys) Get(_ context.Context, subject string) (wrapped []byte, found bool, err error) {
	//: a malformed subject is refused before any lookup.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return nil, false, subjectErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	held, found := m.keys[subject]
	//: a copy, so the caller's slice is not the store's.
	return bytes.Clone(held), found, nil
}

// Insert files wrapped under subject when nothing is filed there.
func (m *memorySubjectKeys) Insert(_ context.Context, subject string, wrapped []byte) (inserted bool, err error) {
	//: a malformed subject is refused before any write.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return false, subjectErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	//: a key is filed already: nothing is stored.
	if _, taken := m.keys[subject]; taken {
		//: an answer, not an error.
		return false, nil
	}
	m.keys[subject] = bytes.Clone(wrapped)
	//: filed.
	return true, nil
}

// Replace files next under subject while subject still holds current.
func (m *memorySubjectKeys) Replace(_ context.Context, subject string, current, next []byte) (replaced bool, err error) {
	//: a malformed subject is refused before any write.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return false, subjectErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held, found := m.keys[subject]
	//: destroyed, or changed, since the caller read it: nothing is stored.
	if !found || !bytes.Equal(held, current) {
		//: an answer: a destroyed key is never brought back.
		return false, nil
	}
	m.keys[subject] = bytes.Clone(next)
	//: swapped.
	return true, nil
}

// Delete removes the key filed under subject. The map drops its only
// reference; the bytes are left to the collector, as everything this process
// held is.
func (m *memorySubjectKeys) Delete(_ context.Context, subject string) (deleted bool, err error) {
	//: a malformed subject is refused before any write.
	if subjectErr := coresecret.ValidateSubject(subject); subjectErr != nil {
		//: InvalidSubject.
		return false, subjectErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	held, found := m.keys[subject]
	//: nothing filed: not an error, and nothing to clear.
	if !found {
		//: nothing deleted.
		return false, nil
	}
	clear(held)
	delete(m.keys, subject)
	//: deleted.
	return true, nil
}

// All yields every filed key, sorted by subject, from a copy taken under the
// read lock — no lock is held while the caller runs, so it may Replace or
// Delete between two keys. A key filed after the copy is not yielded.
func (m *memorySubjectKeys) All(_ context.Context) iter.Seq2[coresecret.SubjectKeyValue, error] {
	//: the copy is taken when the caller starts ranging, not when All is called.
	return func(yield func(coresecret.SubjectKeyValue, error) bool) {
		//: every key, in subject order, stopping when the caller does.
		for _, entry := range m.snapshot() {
			//: the caller stopped ranging.
			if !yield(entry, nil) {
				//: stop.
				return
			}
		}
	}
}

// snapshot copies every filed key, sorted by subject.
func (m *memorySubjectKeys) snapshot() []coresecret.SubjectKeyValue {
	m.mu.RLock()
	defer m.mu.RUnlock()
	subjects := slices.Sorted(maps.Keys(m.keys))
	entries := make([]coresecret.SubjectKeyValue, 0, len(subjects))
	//: each key copied, so the caller never holds the store's slice.
	for _, subject := range subjects {
		entries = append(entries, coresecret.SubjectKeyValue{Subject: subject, Wrapped: bytes.Clone(m.keys[subject])})
	}
	//: sorted, so two passes over one store meet the keys in one order.
	return entries
}
