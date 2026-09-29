// Package kit — authentication: the handler that says who calls, and the user
// it names.
package kit

import (
	"context"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// UID identifies an authenticated user. It is what the auth handler returns
// and what every endpoint reads with [UserID].
type UID = ikit.UID

// Authenticator is the app's authentication handler: it turns the
// credentials of a request — a session cookie, a bearer token — into a user,
// in front of every endpoint declared with [Auth] or [AuthOptional]. An app
// has at most one.
//
// P is the credentials: a struct whose fields are tagged cookie:"…" or
// header:"…", decoded like an endpoint's request. D is what the handler says
// about the user; endpoints read it with [AuthData].
type Authenticator[P, D any] = ikit.AuthenticatorHandler[P, D]

// Auth makes an endpoint require an authenticated caller: the app's auth
// handler runs first, and a request without valid credentials is answered
// 401 before the handler runs. A command or a query that asks for one is
// refused 401 when its caller carries no user: an in-process dispatch
// carries its caller's, and is not authenticated again.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func Auth() OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Auth()
}

// AuthOptional runs the app's auth handler when the request carries
// credentials, and serves anonymous callers too: [UserID] tells which.
//
// IFACE-OPAQUE: the option is sealed — its method is unexported — so a caller
// only hands it to the declaration it configures.
func AuthOptional() OperationOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.AuthOptional()
}

// UserID returns the authenticated caller of the request ctx serves, and
// whether there is one. An in-process [Endpoint].Call carries its caller's
// user along.
func UserID(ctx context.Context) (UID, bool) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.UserID(ctx)
}

// AuthData returns what the auth handler said about the caller, and whether
// there is a caller whose data has type D.
func AuthData[D any](ctx context.Context) (D, bool) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.AuthData[D](ctx)
}

// WithUser returns ctx acting as the given user, with data: what a job, a
// loop or a test uses to call an [Auth] endpoint in-process on someone's
// behalf.
func WithUser[D any](ctx context.Context, uid UID, data D) context.Context {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.WithUser[D](ctx, uid, data)
}
