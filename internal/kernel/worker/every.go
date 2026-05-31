// Package worker — Every layers a ticker loop on top of the LoopDaemon
// lifecycle. Held in its own file so worker.go stays focused on the LoopDaemon
// surface.
package worker

import "time"

// Every starts a LoopDaemon whose loop fires tick on each interval until Stop
// is called. The daemon owns the time.Ticker and stops it when the loop exits,
// so Stop both ends the ticking and joins the goroutine. A non-positive
// interval panics: time.NewTicker itself panics on a non-positive duration, so
// Every surfaces the same contract at its own call site rather than deep in the
// loop.
func Every(interval time.Duration, tick func()) *LoopDaemon {
	//: a nil tick is a programmer error — a ticker that does nothing is a bug.
	if tick == nil {
		//: fail fast at the call site.
		panic("worker: nil tick")
	}
	//: a non-positive interval would panic inside the spawned goroutine; refuse
	//: it here so the panic lands at the caller, not in the daemon.
	if interval <= 0 {
		//: mirror time.NewTicker's own contract at the construction site.
		panic("worker: non-positive interval")
	}
	//: spawn the ticker loop behind the generic LoopDaemon lifecycle.
	return Start(func(stop <-chan struct{}) {
		//: the loop owns the ticker so it is stopped exactly when the loop exits.
		ticker := time.NewTicker(interval)
		//: release the ticker's resources once the loop returns on Stop.
		defer ticker.Stop()
		//: fire tick on each interval; exit promptly when Stop closes stop.
		for {
			select {
			case <-stop:
				//: Stop signalled — return so the daemon's done channel closes.
				return
			case <-ticker.C:
				//: interval elapsed — run the caller's tick.
				tick()
			}
		}
	})
}
