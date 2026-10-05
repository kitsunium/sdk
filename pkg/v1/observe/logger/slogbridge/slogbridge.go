package slogbridge

import (
	"log/slog"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// newHandler is NewHandler's body: decl_gen.go writes NewHandler, from the
// design, as one call of it.
func newHandler(lg logger.Logger) (h slog.Handler, err error) {
	//: refuse the nil Logger explicitly — mirrors NewText's WriterRequired
	//: contract, where defaulting silently was the documented mistake.
	if lg == nil {
		//: surface the documented sentinel so callers can HasCode(err, 1.1.1.1).
		return nil, LoggerRequired
	}
	//: wrap the Logger; every derived handler shares this same destination.
	return handler{lg: lg}, nil
}

// New returns a *slog.Logger writing through lg — the one-liner a caller hands
// to a library whose logging knob is typed as the concrete *slog.Logger.
//
// A nil lg returns [LoggerRequired] (1.1.1.1).
func New(lg logger.Logger) (sl *slog.Logger, err error) {
	//: reuse NewHandler so the nil-check and its sentinel have one home.
	hdl, hErr := NewHandler(lg)
	//: forward the construction error untouched (origin wins).
	if hErr != nil {
		//: the sentinel already carries the right code/reason.
		return nil, hErr
	}
	//: slog.New adds no policy of its own — the handler owns every decision.
	//sdkguard:allow SDK001 this package IS the sanctioned bridge (ADR 0032); the handler it wraps forwards to the SDK Logger
	return slog.New(hdl), nil
}
