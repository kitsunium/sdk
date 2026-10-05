package kit

import (
	"context"
	"runtime/debug"
	"slices"

	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// Activity declares that the service is busy while fn returns true: a daemon
// whose clients are sessions, not connections, stops once the last session
// ended rather than once the last connection closed. fn is called on the
// idle watch's tick, from one goroutine, and must answer quickly; a panic
// counts as busy, and is logged. The value is kept for the declaration:
//
//	var _ = Service.Activity(func(context.Context) bool { return sessions.Open() > 0 })
//
//go:noinline
func (s *Service) Activity(fn func(context.Context) bool) *ActivityHandler {
	p := &ActivityHandler{svc: s, fn: fn, decl: callerPos()}
	if fn == nil {
		s.problem(p.decl, "", "activity.nil", "service", s.name)
		return p
	}
	s.mu.Lock()
	s.activities = append(s.activities, p)
	s.mu.Unlock()
	return p
}

// busy reports whether an activity of a mounted service says the product is
// busy.
func (a *App) busy(ctx context.Context) bool {
	for _, svc := range a.services {
		svc.mu.Lock()
		probes := slices.Clone(svc.activities)
		svc.mu.Unlock()
		for _, p := range probes {
			if p.busy(ctx, a) {
				return true
			}
		}
	}
	return false
}

// busy calls the probe; a panic is busy — stopping a daemon on a probe that
// failed would drop what it serves —, and is logged.
func (p *ActivityHandler) busy(ctx context.Context, a *App) (busy bool) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(ctx, a.log, "an activity probe panicked; the daemon counts as busy", logger.String("service", p.svc.name),
				logger.Any("panic", r), logger.String("stack", string(debug.Stack())))
			busy = true
		}
	}()
	return p.fn(ctx)
}
