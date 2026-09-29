// Package kit — replacements: a test's substitute for an operation.
package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

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
func Replace[Req, Resp any](op ikit.Operation[Req, Resp], fn func(context.Context, Req) (Resp, error)) AppOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Replace[Req, Resp](op, fn)
}
