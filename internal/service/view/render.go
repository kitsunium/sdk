// Package view — the bounded, all-or-nothing render.
package view

import (
	"bytes"
	"context"
	"errors"
	"html/template"

	coreview "github.com/kitsunium/sdk/internal/core/view"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

// initialBufferBytes is the scratch buffer a fresh pool entry starts at: 32 KiB,
// which holds a typical server-rendered page without a single regrowth.
const initialBufferBytes int = 32 << 10

// buffers recycles the scratch a render executes into.
//
// It is a CappedPool (ADR 0010) rather than a bare sync.Pool because the
// ceiling and the pool disagree by three orders of magnitude: a render is
// allowed 8 MiB by default, and one 8 MiB page must not leave 8 MiB pinned for
// the life of the process. Anything over [coreview.MaxPooledBytes] is orphaned
// on Put instead of being repooled.
var buffers = recycler.NewCappedPool(
	func() *bytes.Buffer { return bytes.NewBuffer(make([]byte, 0, initialBufferBytes)) },
	func(b *bytes.Buffer) { b.Reset() },
	func(b *bytes.Buffer) int { return b.Cap() },
	coreview.MaxPooledBytes,
)

// renderer is one parsed template set plus the ceiling its renders are bounded
// by. Immutable after construction: there is no Add, no Reload and no lazy
// parse, so no lock is needed and no request can observe a half-built set.
type renderer struct {
	// set is the parsed tree. Every template in it is named by its slash path.
	set *template.Template
	// maxBytes is the per-render output ceiling, already clamped.
	maxBytes int
	// count is how many templates the set holds, reported in TemplateNotFound
	// so "the renderer is empty" and "that one name is wrong" are one log line
	// apart.
	count int
}

// Render executes the template registered under name against data and returns
// the complete document, or nothing at all.
//
// The order of the four steps is the security property, not an implementation
// detail:
//
//  1. the context is checked, so a request already abandoned does no work;
//  2. the name is resolved, so an unknown page is a typed refusal and not an
//     empty document;
//  3. the model is scanned for the six refused trust types, BEFORE the
//     template runs, so a violation has no partial page to reason about;
//  4. the template executes into a bounded buffer the engine owns.
//
// On any failure the returned slice is nil. The bytes produced before a
// mid-execution failure are discarded inside the engine and never reach the
// caller — which is the entire reason this method returns a slice rather than
// taking an io.Writer.
func (r *renderer) Render(ctx context.Context, name string, data any) (document []byte, err error) {
	//: a request the caller has already abandoned buys no work. ctx.Err() is
	//: returned verbatim: the caller supplied the deadline and already knows
	//: what it means (the resilience and lock precedent).
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: hand the caller's own verdict straight back.
		return nil, ctxErr
	}
	tmpl := r.set.Lookup(name)
	//: an unknown name — including "" — is a loud refusal, never a blank page.
	if name == "" || tmpl == nil {
		//: the name is a Field: it is part of the application's routing
		//: surface and has no business in a Public.
		//: nothing has been rendered, so nothing is returned.
		return nil, raise(coreview.TemplateNotFound, nil,
			errs.String("template", name), errs.Int("templates", r.count))
	}
	//: refuse the model BEFORE execution: after it, a partial page exists.
	if path, typeName, unsafe := scanUnsafe(data); unsafe {
		//: the path and the type, never the value (ADR 0046's rule).
		return nil, raise(coreview.UnsafeValue, nil,
			errs.String("path", path), errs.String("type", typeName))
	}
	//: the render's own scratch, borrowed and returned on every path.
	return r.execute(tmpl, name, data)
}

// execute runs tmpl into a pooled, bounded buffer and copies the result out.
func (r *renderer) execute(tmpl *template.Template, name string, data any) (document []byte, err error) {
	buf := buffers.Get()
	defer buffers.Put(buf)
	writer := &limitWriter{buf: buf, limit: r.maxBytes}
	execErr := tmpl.Execute(writer, data)
	//: a failed execution yields nothing; the partial bytes die with the
	//: buffer, which is why the caller's destination was never passed in.
	if execErr != nil {
		//: classify before reporting — a ceiling breach is not a bug in the
		//: template, and conflating them sends an operator to the wrong file.
		return nil, renderFailure(execErr, name, r.maxBytes)
	}
	//: copy out: the scratch goes back to the pool and must not be aliased by
	//: anything the caller holds.
	document = make([]byte, buf.Len())
	copy(document, buf.Bytes())
	//: the complete document, or nothing — never a prefix.
	return document, nil
}

// renderFailure turns html/template's execution error into the right sentinel.
//
// [coreview.RenderTooLarge] is recognised by identity rather than by message:
// text/template strips its own writeError wrapper and returns the writer's
// error verbatim, so the sentinel [limitWriter] returned arrives here
// unchanged. Matching on a message would break the first time the stdlib
// rephrased one.
func renderFailure(execErr error, name string, limit int) error {
	//: the ceiling breach is a denial-of-service guard, not a template defect.
	if errors.Is(execErr, coreview.RenderTooLarge) {
		//: the engine's own message is not attached: there is none worth
		//: keeping, and the limit and the name say everything.
		return raise(coreview.RenderTooLarge, nil,
			errs.String("template", name), errs.Int("limit", limit))
	}
	//: everything else is a genuine execution failure. The engine's diagnostic
	//: names the file path, the line, the column and a fragment of template
	//: source, so it travels as a Field and never as a Public.
	return raise(coreview.RenderFailed, execErr, errs.String("template", name))
}
