// Package kit — the compile-time proof that a listener is a starter.
package kit

// : Asserts at compile time that *Listener is brought up and taken down with
// the app.
var _ starter = (*Listener)(nil)
