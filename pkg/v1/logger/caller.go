// Package logger — adds the opt-in caller annotation surface. WithCaller
// derives a Logger whose records carry a structured "source" attribute
// (file:line:function) resolved from the program counter the front-end already
// captures. It is additive: an unwrapped Logger emits no source field, so the
// frozen record shape is unchanged until a caller opts in.
package logger

import (
	svclogger "github.com/kitsunium/sdk/internal/service/logger"
)

// WithCaller returns a derived Logger whose emitted records carry a "source"
// attribute resolving the call site into file:line:function. The skip argument
// is the extra stack-frame offset reserved for wrapper layers; pass 0 for
// direct callers. The returned Logger shares the original's sink and encoder;
// the only change is the added annotation, so the option is safe to layer onto
// any Logger built by this package.
//
//	lg, _ := logger.Default()
//	lg = logger.WithCaller(lg, 0)
//	lg.Info(ctx, "ready") // record now carries source=…/main.go:42:main.run
func WithCaller(lg Logger, skip int) Logger {
	//: delegate to the service entry point that owns the handler decoration.
	return svclogger.WithCaller(lg, skip)
}
