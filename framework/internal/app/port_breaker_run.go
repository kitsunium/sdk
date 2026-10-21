package app

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// breakerPoll is how often a running product reads the operator's marks:
// an isolation or a reset made with `breaker` reaches it within this.
const breakerPoll time.Duration = 5 * time.Second

// breakerAt is a port's breaker in an app: the port's ID, the mark that
// isolates it, and the breaker.
type breakerAt struct {
	port string
	mark string
	b    portBreaker
}

// breakers are the breakers of the app's ports.
func (a *App) breakers() []breakerAt {
	var out []breakerAt
	for _, gp := range a.guardedPorts() {
		for _, l := range gp.guards.layers {
			if b, ok := l.(portBreaker); ok {
				out = append(out, breakerAt{port: gp.id, mark: breakerMark(gp.svc.name, gp.name), b: b})
			}
		}
	}
	return out
}

// breakerMark is where, under the data directory, an operator's isolation
// of the breaker of the port name of service is kept.
func breakerMark(service, name string) string {
	return path.Join(service, "ports", name+".isolated")
}

// addBreakers readies the app's breakers once its components are being
// added, through one of them, so that a product that declares none links
// none of it (attach).
func (a *App) addBreakers(cli bool) error {
	if bs := a.breakers(); len(bs) > 0 {
		return bs[0].b.attach(a, bs, cli)
	}
	return nil
}

// attach readies the breakers bs of app a: the operator's marks applied;
// then, unless a CLI command runs, a readiness check each, and the
// component that applies the marks a running product finds.
func (*breakerGuard) attach(a *App, bs []breakerAt, cli bool) error {
	m := &breakerMarks{a: a, breakers: bs, applied: map[string]bool{}}
	m.apply()
	if cli {
		return nil
	}
	for _, b := range bs {
		check := func(context.Context) error { return breakerReady(b.port, b.b.state(a)) }
		if err := a.health.AddReadiness(health.ReadinessCheck{Name: "breaker:" + b.port, NonCritical: true, Check: check}); err != nil {
			return err
		}
	}
	if a.data == nil {
		return nil
	}
	return a.component("breakers", "", m.start, func(context.Context) error { return nil })
}

// breakerMarks applies the operator's marks to the app's breakers: applied
// says which it isolated, so that a mark taken off resets only those.
type breakerMarks struct {
	a        *App
	breakers []breakerAt
	applied  map[string]bool
}

// apply isolates each breaker whose mark appeared, and resets each whose
// mark it applied went away. A mark it cannot read changes nothing.
func (m *breakerMarks) apply() {
	if m.a.data == nil {
		return
	}
	for _, b := range m.breakers {
		_, err := fs.Stat(m.a.data, b.mark)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		marked, st := err == nil, b.b.state(m.a)
		if st == nil || marked == m.applied[b.port] {
			continue
		}
		m.applied[b.port] = marked
		if marked {
			st.Isolate()
			logger.Warn(context.Background(), m.a.log, "an operator isolated the breaker of "+b.port, logger.String("node", b.port))
			continue
		}
		st.Reset()
		logger.Info(context.Background(), m.a.log, "an operator reset the breaker of "+b.port, logger.String("node", b.port))
	}
}

// start reads the marks every breakerPoll on the app's clock, until the app
// stops.
//
// Goroutine lifecycle: one tracked goroutine, which ends when the app's base
// context ends; the stop waits for it.
func (m *breakerMarks) start(_ context.Context) error {
	ok := m.a.goTracked(func() {
		t := m.a.clock.NewTicker(breakerPoll)
		defer t.Stop()
		for {
			select {
			case <-m.a.baseCtx.Done():
				return
			case _, open := <-t.C():
				if !open {
					return
				}
				m.apply()
			}
		}
	})
	if !ok {
		return failure(CodeAppRunning, "APP_STOPPING", "the app is stopping", nil)
	}
	return nil
}
