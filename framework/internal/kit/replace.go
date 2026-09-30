// Package kit — replacements: a test's substitute for an operation.
package kit

import (
	"cmp"
	"context"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
)

// testBinary reports whether the program is a test binary, the one place a
// replacement may run: testing.Testing, behind a variable an internal test
// sets to prove the refusal. It is why kit's non-test code imports testing.
var testBinary = testing.Testing

// replaceDecl is one kit.Replace the app was given.
type replaceDecl struct {
	op node
	// fn is a func(context.Context, Req) (Resp, error) of op's types, or nil.
	fn any
	at pos
}

// replaceOption is kit.Replace's AppOption.
type replaceOption struct{ decl replaceDecl }

// appConfigure sets the option on what it configures.
func (r *replaceOption) appConfigure(o *appOptions) { o.replaces = append(o.replaces, r.decl) }

// Replace runs fn in place of op's code, in the app it is given to: an
// endpoint's handler — or, with ADR 0005, a command's or a query's — or what
// a port calls. It replaces the code, not the mechanics: authentication,
// decoding, validation and the policies still run, and a replaced port needs
// no binding, so a module's tests run without a host. fn takes op's request
// and returns its response, or it does not compile.
//
// A replaced run's span carries mock: replace, and runtime.mocks lists the
// replacement. It is for tests: the start refuses it in a program go test
// did not build — neither a production binary nor kit dev can carry one.
// Given twice for one operation, the last wins.
//
//	app := App.With(kit.Replace(apps.AppAPI, func(ctx context.Context, in apps.IDInput) (apps.Profile, error) {
//		return apps.Profile{ID: in.AppID}, nil
//	}))
//
//go:noinline
func Replace[Req, Resp any](op Operation[Req, Resp], fn func(context.Context, Req) (Resp, error)) AppConfigurer {
	d := replaceDecl{at: callerPos()}
	if op != nil {
		d.op = op
	}
	if fn != nil {
		d.fn = fn
	}
	return &replaceOption{decl: d}
}

// replacement is a test's function in force in a running app.
type replacement struct {
	fn   any
	hits atomic.Int64
}

// replacements are the app's replacements that can run, by node — the last
// one of a node winning — and every problem with the others.
func (a *App) replacements() (map[node]*replacement, []model.Diagnostic) {
	mounted := map[*Service]bool{}
	for _, svc := range a.services {
		if svc != nil {
			mounted[svc] = true
		}
	}
	var problems []model.Diagnostic
	out := map[node]*replacement{}
	for _, r := range a.opts.replaces {
		if refused, problem := replaceProblem(&r, mounted); !problem.empty() {
			id := ""
			if refused != nil {
				id = refused.base().id
			}
			problems = append(problems, diagnosticOf("error", id, a.source(&r.at), problem))
			continue
		}
		out[r.op] = &replacement{fn: r.fn}
	}
	return out, problems
}

// replaceProblem is what is wrong with a replacement, and the node it names
// when it names one; empty when the replacement holds.
func replaceProblem(r *replaceDecl, mounted map[*Service]bool) (node, phrase) {
	switch {
	case r.op == nil || nilOperation(r.op):
		return nil, say("replace.nil")
	case !testBinary():
		return r.op, say("replace.not-test", "node", r.op.base().id)
	case r.fn == nil || reflect.ValueOf(r.fn).IsNil():
		return r.op, say("replace.nil-fn", "node", r.op.base().id)
	case !mounted[r.op.base().svc]:
		return r.op, say("replace.unmounted", "node", r.op.base().id, "service", r.op.base().svc.name)
	default:
		return r.op, phrase{}
	}
}

// replaceProblems is every problem with the app's replacements.
func (a *App) replaceProblems() []model.Diagnostic {
	_, problems := a.replacements()
	return problems
}

// replaced is the function a test gave the running app in place of n's code
// — counted, marked on n's span, and answering a panic as a handler does —
// or nil. Every operation's run asks it where its handler would run.
func replaced[Req, Resp any](ctx context.Context, a *App, n node) func(context.Context, Req) (Resp, error) {
	if a == nil {
		return nil
	}
	w := a.wired.Load()
	if w == nil || len(w.replaced) == 0 {
		return nil
	}
	r := w.replaced[n]
	if r == nil {
		return nil
	}
	fn, ok := r.fn.(func(context.Context, Req) (Resp, error))
	if !ok {
		return nil
	}
	r.hits.Add(1)
	markMocked(ctx, n.base().id, model.MockReplace)
	return func(ctx context.Context, req Req) (resp Resp, err error) {
		defer func() {
			if p := recover(); p != nil {
				err = panicked(ctx, a, n.base().id, "replacement panicked", p)
			}
		}()
		return fn(ctx, req)
	}
}

// mockList is every replacement a test gave the app, by node — the Studio
// shows each on its node and never edits one; nil without any.
func (a *App) mockList() []model.Mock {
	w := a.wired.Load()
	if w == nil {
		return []model.Mock{}
	}
	out := make([]model.Mock, 0, len(w.replaced))
	for n, r := range w.replaced {
		out = append(out, model.Mock{Node: n.base().id, Mode: model.MockReplace, Hits: r.hits.Load()})
	}
	slices.SortFunc(out, func(x, y model.Mock) int { return cmp.Or(cmp.Compare(x.Node, y.Node), cmp.Compare(x.Mode, y.Mode)) })
	return out
}

// markMocked tags the span of node running in ctx with the replacement that
// changed it: the trace shows the run was not the product's own.
func markMocked(ctx context.Context, node, mode string) { markSpan(ctx, node, "mock", mode) }

// markSpan sets an attribute of the span of node ctx runs inside, in dev.
func markSpan(ctx context.Context, node, key, value string) {
	if sp := spanOf(ctx); sp != nil && sp.s.Node == node {
		sp.attr(key, value)
	}
}
