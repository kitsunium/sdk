//go:build linux

package service

import (
	"testing"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The replay of Type=dbus (S0e, kitsunium/sdk#737): the reducer driven by
// the owners the bus announces, as service.c's service_bus_name_owner_change,
// service_good and service_enter_running read them (systemd 262).

// dbusHarness is a Type=dbus unit's replay, the watch played by the harness.
func dbusHarness(t *testing.T, edit func(*Config)) *harness {
	t.Helper()
	cfg := dbusConfig(t)
	if edit != nil {
		edit(&cfg)
	}
	return newHarness(t, cfg)
}

// TestDbusReadyOnOwner: the start waits in start with the main process
// running until the bus announces an owner; then the unit is up and the start
// job answered. The watch began at the start's first spawn, before the main
// process, and looked the owner up once.
func TestDbusReadyOnOwner(t *testing.T) {
	h := dbusHarness(t, nil)
	id := h.start()
	if h.prepares != 1 || h.busLookups != 1 {
		t.Fatalf("preparations %d, lookups %d: want the watch begun at the first spawn", h.prepares, h.busLookups)
	}
	h.want(coresvc.StateActivating, coresvc.SubStart)
	if h.m.mainPID == 0 {
		t.Fatal("no main process while waiting for the name")
	}
	h.wantWaiting(id)
	h.bus(true)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
}

// TestDbusOwnedBeforeTheSpawnIsNoReadiness: a name owned when the watch
// began — a stale owner, say — is the level, not readiness: systemd waits in
// start for a change, so does the engine, until TimeoutStartSec= fails it;
// a change announced after the spawn makes the unit ready.
func TestDbusOwnedBeforeTheSpawnIsNoReadiness(t *testing.T) {
	h := dbusHarness(t, func(c *Config) { c.TimeoutStart = 5 * time.Second })
	h.busOwned = true
	id := h.start()
	h.want(coresvc.StateActivating, coresvc.SubStart)
	if !h.m.has(flagBusOwned) {
		t.Fatal("the lookup's owner was not kept as the level")
	}
	main := h.m.mainPID
	h.advance(5 * time.Second)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	h.kill(main, sigTERM)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s, want timeout", h.m.result)
	}

	r := dbusHarness(t, nil)
	r.busOwned = true
	rid := r.start()
	r.bus(false)
	r.want(coresvc.StateActivating, coresvc.SubStart)
	r.bus(true)
	r.want(coresvc.StateActive, coresvc.SubRunning)
	r.wantReply(rid, 0)
}

// TestDbusStartTimeout: a name never owned fails the start after
// TimeoutStartSec=, TimeoutStartFailureMode= terminating it, with the result
// timeout — which Restart=on-failure restarts.
func TestDbusStartTimeout(t *testing.T) {
	h := dbusHarness(t, func(c *Config) {
		c.TimeoutStart, c.Restart = 3*time.Second, coresvc.RestartOnFailure
	})
	id := h.start()
	main := h.m.mainPID
	h.advance(3 * time.Second)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	if got := h.sentTo(main); len(got) == 0 || got[0] != sigTERM {
		t.Fatalf("signals %v, want SIGTERM", got)
	}
	h.kill(main, sigTERM)
	h.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s, want timeout", h.m.result)
	}
}

// TestDbusNameLostStops: running, a loss the bus announces stops the unit at
// once — ExecStop=, then KillSignal= — with the result success, so
// Restart=on-success restarts it and on-failure does not; under
// RemainAfterExit= the unit is exited instead, as service_enter_exited_or_stop
// leaves it.
func TestDbusNameLostStops(t *testing.T) {
	h := dbusHarness(t, func(c *Config) {
		c.Restart, c.ExecStop = coresvc.RestartOnSuccess, []coresvc.CommandValue{cmd(t, "/bin/stop")}
	})
	h.start()
	h.bus(true)
	main := h.m.mainPID
	h.bus(false)
	h.want(coresvc.StateDeactivating, coresvc.SubStop)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	h.kill(main, sigTERM)
	h.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	if h.m.result != coresvc.ResultSuccess {
		t.Fatalf("result %s, want success", h.m.result)
	}

	f := dbusHarness(t, func(c *Config) { c.Restart = coresvc.RestartOnFailure })
	f.start()
	f.bus(true)
	fmain := f.m.mainPID
	f.bus(false)
	f.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	f.kill(fmain, sigTERM)
	f.want(coresvc.StateInactive, coresvc.SubDead)

	r := dbusHarness(t, func(c *Config) { c.RemainAfterExit = true })
	r.start()
	r.bus(true)
	r.bus(false)
	r.want(coresvc.StateExited, coresvc.SubExited)
}

// TestDbusMainEndsBeforeItsName: the main process gone before the name is
// owned goes where service_enter_running sends it — a clean end stops the
// unit, an unclean one fails it.
func TestDbusMainEndsBeforeItsName(t *testing.T) {
	h := dbusHarness(t, nil)
	id := h.start()
	h.exit(h.m.mainPID, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(id, 0)

	f := dbusHarness(t, nil)
	fid := f.start()
	f.exit(f.m.mainPID, 3)
	f.want(coresvc.StateFailed, coresvc.SubFailed)
	f.wantReply(fid, coresvc.CodeStartFailed)
	if f.m.result != coresvc.ResultExitCode {
		t.Fatalf("result %s, want exit-code", f.m.result)
	}
}

// TestDbusRevalidation: a unit that comes up without its name — lost while
// ExecStartPost= ran — is active, running-revalidating, its start job done,
// and given two seconds: the name back makes it running; the grace over, it
// stops; a loss the bus announces meanwhile stops it at once.
func TestDbusRevalidation(t *testing.T) {
	post := func(c *Config) { c.ExecStartPost = []coresvc.CommandValue{cmd(t, "/bin/post")} }
	h := dbusHarness(t, post)
	id := h.start()
	h.bus(true)
	h.want(coresvc.StateActivating, coresvc.SubStartPost)
	h.bus(false)
	h.want(coresvc.StateActivating, coresvc.SubStartPost)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunningRevalidating)
	h.wantReply(id, 0)
	if got := h.deadline(timerState); got != busNameGrace {
		t.Fatalf("grace %s, want %s", got, busNameGrace)
	}
	h.advance(time.Second)
	h.bus(true)
	h.want(coresvc.StateActive, coresvc.SubRunning)

	g := dbusHarness(t, post)
	g.start()
	g.bus(true)
	g.bus(false)
	g.exit(g.m.controlPID, 0)
	main := g.m.mainPID
	g.advance(busNameGrace)
	g.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	g.kill(main, sigTERM)
	g.want(coresvc.StateInactive, coresvc.SubDead)

	a := dbusHarness(t, post)
	a.start()
	a.bus(true)
	a.bus(false)
	a.exit(a.m.controlPID, 0)
	a.want(coresvc.StateActive, coresvc.SubRunningRevalidating)
	a.bus(false)
	a.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
}

// TestDbusLookupWhileRunning: a lookup that finds no owner while the unit
// runs — the first answer of a watch begun then, what S0h's restored unit
// and a reconnected watcher deliver — is not definitive: the unit gets the
// grace, RuntimeMaxSec= paused for the time it spent awaiting its name, as
// systemd re-arms the budget left; an owner looked up keeps it running.
func TestDbusLookupWhileRunning(t *testing.T) {
	h := dbusHarness(t, func(c *Config) { c.RuntimeMax = 10 * time.Second })
	h.start()
	h.bus(true)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.advance(4 * time.Second)
	h.feed(event{kind: evBusName, flags: flagLookup})
	h.want(coresvc.StateActive, coresvc.SubRunningRevalidating)
	h.advance(time.Second)
	h.feed(event{kind: evBusName, flags: flagLookup | flagOwner})
	h.want(coresvc.StateActive, coresvc.SubRunning)
	if got := h.deadline(timerState); got != 6*time.Second {
		t.Fatalf("runtime left %s, want 6s: the second awaiting the name is not the run's", got)
	}
	h.feed(event{kind: evBusName, flags: flagLookup | flagOwner})
	h.want(coresvc.StateActive, coresvc.SubRunning)
}

// TestDbusWatchEndKeepsTheLevel: the watch's end — the connection to the bus
// lost — changes nothing: the unit runs on, the next start watches again.
func TestDbusWatchEndKeepsTheLevel(t *testing.T) {
	h := dbusHarness(t, nil)
	h.start()
	h.bus(true)
	h.feed(event{kind: evBusName, flags: flagUnwatched})
	h.want(coresvc.StateActive, coresvc.SubRunning)
	if !h.m.has(flagBusOwned) {
		t.Fatal("the level was lost with the watch")
	}
	h.stopAndExit()
	h.busWatched = false
	h.busOwned = false
	h.start()
	if h.busLookups != 2 || h.m.has(flagBusOwned) {
		t.Fatalf("lookups %d, owned %v: want the next start's lookup to set the level", h.busLookups, h.m.has(flagBusOwned))
	}
}

// TestDbusRestartWatchesOnce: a restart prepares its first spawn again, but
// the running watch is not looked up again; the owner the bus announces
// after the restart's spawn makes it ready.
func TestDbusRestartWatchesOnce(t *testing.T) {
	h := dbusHarness(t, nil)
	h.start()
	h.bus(true)
	main := h.m.mainPID
	id := h.request(evRestart)
	h.kill(main, sigTERM)
	h.bus(false)
	h.want(coresvc.StateActivating, coresvc.SubStart)
	h.bus(true)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	if h.prepares != 2 || h.busLookups != 1 {
		t.Fatalf("preparations %d, lookups %d: want one watch across the restart", h.prepares, h.busLookups)
	}
}

// TestDbusRevalidatingUnitStopsAndReloads: running-revalidating is up — a
// stop runs ExecStop=, a reload is taken, the watchdog runs — as systemd
// treats SERVICE_RUNNING_REVALIDATING as running.
func TestDbusRevalidatingUnitStopsAndReloads(t *testing.T) {
	h := dbusHarness(t, func(c *Config) {
		c.ExecReload = []coresvc.CommandValue{cmd(t, "/bin/reload")}
		c.ExecStop = []coresvc.CommandValue{cmd(t, "/bin/stop")}
	})
	h.start()
	h.bus(true)
	h.feed(event{kind: evBusName, flags: flagLookup})
	h.want(coresvc.StateActive, coresvc.SubRunningRevalidating)
	if !watchdogRuns(h.m.sub) {
		t.Fatal("the watchdog does not run while revalidating")
	}
	rid := h.request(evReload)
	h.want(coresvc.StateReloading, coresvc.SubReload)
	h.bus(true)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(rid, 0)
	h.feed(event{kind: evBusName, flags: flagLookup})
	sid := h.request(evStop)
	h.want(coresvc.StateDeactivating, coresvc.SubStop)
	h.wantWaiting(sid)
}
