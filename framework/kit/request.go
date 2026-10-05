package kit

import (
	"context"
	"net/http"
	"time"

	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// setCookie is SetCookie's body: decl_gen.go writes SetCookie, from the
// design, as one call of it.
func setCookie(ctx context.Context, c *http.Cookie) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	ikit.SetCookie(ctx, c)
}

// clearCookie is ClearCookie's body: decl_gen.go writes ClearCookie, from the
// design, as one call of it.
func clearCookie(ctx context.Context, name string) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	ikit.ClearCookie(ctx, name)
}

// clientIP is ClientIP's body: decl_gen.go writes ClientIP, from the
// design, as one call of it.
func clientIP(ctx context.Context) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.ClientIP(ctx)
}

// userAgent is UserAgent's body: decl_gen.go writes UserAgent, from the
// design, as one call of it.
func userAgent(ctx context.Context) string {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.UserAgent(ctx)
}

// now is Now's body: decl_gen.go writes Now, from the
// design, as one call of it.
func now(ctx context.Context) time.Time {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Now(ctx)
}

// newTicker is NewTicker's body: decl_gen.go writes NewTicker, from the
// design, as one call of it.
func newTicker(ctx context.Context, d time.Duration) clock.Ticker {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NewTicker(ctx, d)
}

// after is After's body: decl_gen.go writes After, from the
// design, as one call of it.
func after(ctx context.Context, d time.Duration) <-chan time.Time {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.After(ctx, d)
}

// log is Log's body: decl_gen.go writes Log, from the
// design, as one call of it.
func log(ctx context.Context) logger.Logger {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Log(ctx)
}
