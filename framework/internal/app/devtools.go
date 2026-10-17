package app

import (
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// What the Studio's dev tools read, in dev (studio_host.go): the process,
// the logs, the mailbox, a field's former values, a record's versions, a
// store's trash. Their routes are framework/app/studio's (ADR 0180 step 1);
// none of them exists outside dev (invariant 4), and none of them acts on the
// product (D13, ADR 0147 §8).

// goTracked runs fn on a goroutine Stop waits for (invariant 9). It refuses,
// and reports false, once Stop has begun.
func (a *App) goTracked(fn func()) bool {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return false
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		fn()
	}()
	return true
}

// The process ---------------------------------------------------------------

// processSample is a sample of the running process from the sampler
// framework/app/server plugs in (plug.SampleProcess), nil without one: kit
// itself imports neither runtime/metrics nor the SDK's process package, whose
// initialisation a CLI or a daemon would pay for nothing (ADR 0172).
func processSample() *model.Process {
	sample := plug.SampleProcess.Load()
	if sample == nil {
		return nil
	}
	return (*sample)()
}

// The mailbox ---------------------------------------------------------------

// mailboxes returns every node that keeps mail.
func (a *App) mailboxes() []mailbox {
	var out []mailbox
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if mb, ok := n.(mailbox); ok {
				out = append(out, mb)
			}
		}
	}
	return out
}
