package service

import (
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The shell's part of Type=dbus (S0e, kitsunium/sdk#737): the watch of
// BusName= through Config.BusWatcher. The reducer decides; the bus is reached
// here alone.

// prepareStart readies what a start's first spawn needs before it runs: the
// unit's group (S0g), then a Type=dbus unit's name watch (S0e), whose first
// owner comes back as flagLookup. Any refusal fails the spawn.
func (a *actor) prepareStart() (eventFlag, error) {
	//: the group: watched, delegated, set.
	if err := a.prepare(); err != nil {
		//: the refusal.
		return 0, err
	}
	//: the bus name.
	return a.startBusWatch()
}

// startBusWatch watches a Type=dbus unit's name unless the watch runs. The
// owner the watch looked up first is the level before the main process runs,
// returned as flagLookup and flagOwner — no readiness: only a change the bus
// announces after the spawn makes the unit ready, as systemd waits for one.
// One goroutine forwards every later change to the actor.
func (a *actor) startBusWatch() (eventFlag, error) {
	s := a.u.s
	//: no name to watch, or watched already.
	if s.typ != coresvc.TypeDbus || a.on(shellBusWatching) {
		//: nothing looked up.
		return 0, nil
	}
	owners, err := a.u.cfg.BusWatcher.WatchName(a.u.ctx, s.busName)
	//: the bus cannot be asked: the spawn's failure, as systemd's unit fails
	//: whose name it cannot watch.
	if err != nil {
		//: the refusal.
		return 0, err
	}
	owner, open := <-owners
	//: the connection went before its first answer: watched again at the
	//: next start, the level as it was.
	if !open {
		//: nothing looked up.
		return 0, nil
	}
	a.put(shellBusWatching, true)
	a.u.readers.Add(1)
	//: Goroutine lifecycle: ended when the watcher closes its channel — the
	//: unit's ctx ended (Close) or the connection to the bus was lost —;
	//: joined by Close through readers.
	go a.u.forwardBus(owners)
	flags := flagLookup
	//: the name has an owner now.
	if owner != "" {
		flags |= flagOwner
	}
	//: the level.
	return flags, nil
}

// forwardBus posts every owner the watch announces to the actor, then the
// watch's end. Once the actor ended it drains the channel, so a watcher never
// waits on it, until the watcher closes it with the unit's ctx.
func (u *Unit) forwardBus(owners <-chan string) {
	defer u.readers.Done()
	posting := true
	//: until the watch closes.
	for owner := range owners {
		ev := event{kind: evBusName}
		//: the name has an owner.
		if owner != "" {
			ev.flags = flagOwner
		}
		posting = posting && u.post(&ev)
	}
	//: the connection is gone: the level stands until the next start.
	if posting {
		u.post(&event{kind: evBusName, flags: flagUnwatched})
	}
}
