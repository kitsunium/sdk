// Package docstore — the functions told about a write or a deletion once it
// is durable, outside every lock.
package docstore

import (
	"slices"
	"sync"
)

// hook is one registered function and the identity its remover finds it by.
type hook struct {
	// fn is told the key.
	fn func(key string)
	// id identifies the registration.
	id uint64
}

// hooks is an ordered set of functions called with a key. Registration and
// removal may happen at any moment, concurrently with calls.
type hooks struct {
	// fns are the registered functions, in registration order.
	fns []hook
	// mu guards fns and next.
	mu sync.Mutex
	// next is the identity the next registration gets.
	next uint64
}

// add registers fn and returns the function that removes it. A nil fn
// registers nothing.
func (h *hooks) add(fn func(key string)) (remove func()) {
	//: nothing to call.
	if fn == nil {
		//: a remover with nothing to remove.
		return func() {}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.next
	h.next++
	h.fns = append(h.fns, hook{fn: fn, id: id})
	//: removes exactly this registration, once or many times.
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.fns = slices.DeleteFunc(h.fns, func(registered hook) bool { return registered.id == id })
	}
}

// call tells every registered function about key, in registration order, on
// the caller's goroutine and outside the lock — a hook may register, remove,
// or use the store it is told about.
func (h *hooks) call(key string) {
	h.mu.Lock()
	fns := slices.Clone(h.fns)
	h.mu.Unlock()
	//: in registration order.
	for _, registered := range fns {
		registered.fn(key)
	}
}

// OnWrite registers fn to be told the key of every document Put, Insert,
// Replace or Update stored, once the write is durable, on the writer's
// goroutine and outside every lock. It returns the function that removes the
// registration. A panic in fn reaches the writer after its write stood.
func (s *Store[T]) OnWrite(fn func(key string)) (remove func()) {
	//: the write hooks.
	return s.onWrite.add(fn)
}

// OnDelete registers fn to be told the key of every document Delete removed,
// on the same terms as OnWrite.
func (s *Store[T]) OnDelete(fn func(key string)) (remove func()) {
	//: the deletion hooks.
	return s.onDelete.add(fn)
}
