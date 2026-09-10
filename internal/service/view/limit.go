// Package view — the writer that stops a render AT its ceiling rather than
// after it.
package view

import (
	"bytes"

	coreview "github.com/kitsunium/sdk/internal/core/view"
)

// limitWriter is the io.Writer html/template executes into. It accumulates
// into a buffer the engine owns and refuses the write that would take the
// document past its ceiling.
//
// The refusal is the whole point of the type. text/template's Execute takes no
// context and cannot be cancelled, and the stdlib's only built-in guard is a
// recursion DEPTH limit — a {{range}} over an attacker-influenced collection
// has no depth at all and will write until the machine stops. The byte ceiling
// is the only bound that exists, so it is enforced at the one place every byte
// passes through.
//
// It stops AT the ceiling rather than after it: the over-long write is never
// copied into the buffer, so the process does not have to survive the
// allocation in order to reject it.
type limitWriter struct {
	// buf accumulates the document. Owned by the engine, borrowed from the
	// pool, and never handed to the caller — Render copies out.
	buf *bytes.Buffer
	// limit is the already-clamped [coreview.Config.MaxBytes] ceiling.
	limit int
}

// Write appends p, or refuses it whole when it would breach the ceiling.
//
// The error returned is the [coreview.RenderTooLarge] sentinel VALUE, not a
// wrap of it: text/template strips its own writeError wrapper and hands the
// writer's error back from Execute verbatim, so returning the sentinel is what
// lets Render tell a ceiling breach from a genuine execution failure with
// errors.Is rather than by matching a message.
func (w *limitWriter) Write(p []byte) (n int, err error) {
	//: refuse the whole write rather than truncate — a half-written document
	//: is discarded anyway, and copying it first buys nothing but the
	//: allocation this ceiling exists to prevent.
	if w.buf.Len()+len(p) > w.limit {
		//: the sentinel travels unwrapped; Execute hands it back verbatim.
		return 0, coreview.RenderTooLarge
	}
	//: bytes.Buffer.Write never fails; the pair is returned for the contract.
	return w.buf.Write(p)
}
