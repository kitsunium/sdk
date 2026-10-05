package kit

import (
	"context"
	"net/http"
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// SetCookie adds a Set-Cookie header to the HTTP response of the endpoint
// ctx serves. kit fills what a cookie left zero with safe defaults — Path
// "/", HttpOnly, SameSite Lax, and Secure outside dev — so a session cookie
// is hardened unless the code says otherwise. In an in-process
// [Endpoint].Call there is no response: the cookie is dropped.
//
// HttpOnly is always set, and Secure always outside dev (and in dev for
// SameSite=None, which browsers require): a boolean has no "unset" a default
// could fill, and kit chooses the hardened reading. A cookie set more than
// once in one response keeps its last value, and cookies set before an error
// are sent with the error — clearing a stale session on a 401 works.
func SetCookie(ctx context.Context, c *http.Cookie) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	ikit.SetCookie(ctx, c)
}

// ClearCookie tells the browser to forget the cookie called name.
func ClearCookie(ctx context.Context, name string) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	ikit.ClearCookie(ctx, name)
}

// ClientIP returns the address of the client of the request ctx serves: the
// connection's peer, or — only when KIT_TRUST_PROXY=on — the last address of
// X-Forwarded-For, the one the trusted proxy appended. It is "" outside an HTTP request.
func ClientIP(ctx context.Context) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.ClientIP(ctx)
}

// UserAgent returns the User-Agent of the request ctx serves, clipped.
func UserAgent(ctx context.Context) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.UserAgent(ctx)
}

// Now returns the app's time: the system clock, or the clock a test gave
// with [Clock]. Product code that compares with a deadline reads it rather
// than time.Now, so that a test's manual clock reaches it.
func Now(ctx context.Context) time.Time {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Now(ctx)
}

// NewTicker is a ticker of period d on the app's clock, for a loop written by
// hand ([Service].Go): a test's manual clock drives it like every timer kit
// runs itself, where a time.Ticker would tick on the wall clock whatever the
// test does. It panics on a non-positive d, as time.NewTicker does.
func NewTicker(ctx context.Context, d time.Duration) clock.Ticker {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewTicker(ctx, d)
}

// After is time.After on the app's clock.
func After(ctx context.Context, d time.Duration) <-chan time.Time {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.After(ctx, d)
}

// Log returns the app's logger, for the node ctx runs inside. In dev, every
// record it writes inside a span is also kept beside that span, so the
// Studio shows a request's logs with its trace.
func Log(ctx context.Context) logger.Logger {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Log(ctx)
}
