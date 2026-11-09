package service

import (
	"os/user"
	"strconv"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The shell's part of the cgroup modes (S0g, kitsunium/sdk#734): the group's
// watch, its reads whenever the reducer decides on it, its signals, its
// delegation and its cleanup. The reducer asks; the group is touched here
// alone.

// prepare readies the unit's group before a start's first spawn, as systemd
// realises a unit's cgroup: its watch running, Delegate='s handing over, and
// memory.oom.group for OOMPolicy=kill. Any refusal fails the spawn.
func (a *actor) prepare() error {
	g := a.group
	//: no group to prepare.
	if g == nil {
		//: nothing to do.
		return nil
	}
	//: the watch, once per unit, again after the group went.
	if err := a.startWatch(); err != nil {
		//: CGROUP_READ_FAILED: a group that cannot be watched.
		return err
	}
	//: Delegate=: the group handed to the service's user.
	if a.u.s.on(setDelegate) {
		//: refused, or a user or group that does not resolve.
		if err := a.delegate(g); err != nil {
			//: the refusal.
			return err
		}
	}
	//: OOMPolicy=kill: the kernel ends the whole group.
	if a.u.s.oomPolicy == coresvc.OOMPolicyKill {
		//: the file refused: no memory controller for the group.
		return g.SetMemoryOOMGroup(true)
	}
	//: ready.
	return nil
}

// delegate hands g to the user and the group the unit's processes run as;
// the supervisor's own keeps it as it is.
func (a *actor) delegate(g coresvc.CgroupUnit) error {
	uid, gid, err := delegateIDs(&a.u.cfg.Spec)
	//: a user or a group that does not resolve.
	if err != nil {
		//: UNKNOWN_USER or UNKNOWN_GROUP.
		return err
	}
	//: the supervisor's own: already its owner.
	if uid == -1 && gid == -1 {
		//: nothing to change.
		return nil
	}
	//: chown of the group and of its delegated files.
	return g.Delegate(uid, gid)
}

// startWatch starts the group's watch unless it runs: the count of OOM kills
// when it starts is the baseline — a kill counted before is no news, and a
// group that went and came back counts from its own start —, then one
// goroutine forwards every later state to the actor.
func (a *actor) startWatch() error {
	//: running.
	if a.on(shellWatching) {
		//: nothing to start.
		return nil
	}
	states, err := a.group.Watch(a.u.ctx)
	//: the group cannot be watched.
	if err != nil {
		//: CGROUP_READ_FAILED.
		return err
	}
	first, open := <-states
	a.oomSeen = first.MemoryEvents.OOMKill
	//: the counters read now, past the first state's.
	if stat, serr := a.group.Stat(); serr == nil {
		a.oomSeen = max(a.oomSeen, stat.MemoryEvents.OOMKill)
	}
	//: the group went before its first state was read.
	if !open {
		//: watched again at the next start.
		return nil
	}
	a.put(shellWatching, true)
	a.u.readers.Add(1)
	//: Goroutine lifecycle: ended when the watch closes its channel — the
	//: unit's ctx ended (Close) or the group went —; joined by Close through
	//: readers.
	go a.u.forwardGroup(states)
	//: watched.
	return nil
}

// forwardGroup posts every state the watch publishes to the actor, and a
// last event once the watch ended while the unit runs.
func (u *Unit) forwardGroup(states <-chan coreproc.GroupEventsValue) {
	defer u.readers.Done()
	//: until the watch closes.
	for state := range states {
		ev := event{kind: evCgroup, oomKills: state.MemoryEvents.OOMKill, flags: flagEmpty}
		//: what the watch read; the actor reads the group again.
		if state.Populated {
			ev.flags = flagPopulated
		}
		//: the actor ended.
		if !u.post(&ev) {
			return
		}
	}
	//: the group went: no process can be left in it.
	u.post(&event{kind: evCgroup, flags: flagEmpty | flagUnwatched})
}

// annotateGroup reads the unit's group with an event the reducer may decide
// on: an OOM kill it was not told of (flagOOM), and the population, read
// again rather than taken from a state the watch read earlier — a late event
// never ends a unit early. A group that cannot be read holds nothing the
// engine could wait for.
func (a *actor) annotateGroup(ev *event) {
	//: no group.
	if a.group == nil {
		return
	}
	//: the watch ended: started again at the next start.
	if ev.has(flagUnwatched) {
		a.put(shellWatching, false)
	}
	kills := ev.oomKills
	//: the counters read now.
	if stat, err := a.group.Stat(); err == nil {
		kills = max(kills, stat.MemoryEvents.OOMKill)
	}
	//: a kill the reducer was not told of.
	if kills > a.oomSeen {
		a.oomSeen = kills
		ev.flags |= flagOOM
	}
	_, population := a.members()
	//: unreadable: the group is gone.
	if population == 0 {
		population = flagEmpty
	}
	ev.flags = ev.flags&^(flagPopulated|flagEmpty) | population
}

// signalGroup sends ef's signals to the unit's group: the main and control
// processes through their handles where they are outside it, then every
// process of it through Signal — passes over the subtree's cgroup.procs until
// none is new, as systemd's cg_kill_recursive —, each process once. SIGKILL
// too, never cgroup.kill (CgroupKill.Kill): on Linux 6.12 one cgroup.kill
// makes every child later cloned into the group (CLONE_INTO_CGROUP) die at
// its fork (measured on 6.12.101), and the unit's next start spawns into it
// again. The outcome says whether the group held a process before the signal.
func (a *actor) signalGroup(ef *effect) event {
	ev := event{kind: evSignaled, sub: ef.sub}
	_, population := a.members()
	//: unreadable: the group is gone.
	if population == 0 {
		population = flagEmpty
	}
	ev.flags |= population
	cg := a.u.cfg.Cgroup
	//: the main and control processes the group's signal would miss.
	for _, pid := range ef.pids {
		p := a.procs[pid]
		//: none, or one the group's signal reaches.
		if p == nil || belongs(cg, pid) {
			continue
		}
		//: each signal through its handle.
		for _, sig := range ef.signals {
			gone(p.Signal(sig))
		}
	}
	//: the group, signal after signal.
	for _, sig := range ef.signals {
		gone(cg.Signal(sig))
	}
	//: the population, for the reducer.
	return ev
}

// prune removes what a delegated service made beneath the unit's group; a
// group still held — a process KillMode=process or none left behind — stays,
// as systemd leaves a cgroup it cannot trim.
func (a *actor) prune() {
	//: no group.
	if a.group == nil {
		return
	}
	gone(a.group.DeleteBeneath())
}

// delegateIDs resolves the user and the group spec runs as into the ids
// Delegate= hands the group to: -1 for the supervisor's own (chown(2)'s
// "unchanged"), the user's primary group when spec names none.
func delegateIDs(spec *coreproc.Spec) (uid, gid int, err error) {
	uid, gid = -1, -1
	//: a user: its uid and primary group.
	if spec.User != "" {
		u, lerr := lookupUser(spec.User)
		//: no such user.
		if lerr != nil {
			//: UNKNOWN_USER.
			return 0, 0, errs.Wrap(coreproc.UnknownUser, errs.WrapParams{}, errs.String("user", spec.User))
		}
		uid, gid = u.uid, u.gid
	}
	//: a group of its own.
	if spec.Group != "" {
		g, lerr := lookupGroup(spec.Group)
		//: no such group.
		if lerr != nil {
			//: UNKNOWN_GROUP.
			return 0, 0, errs.Wrap(coreproc.UnknownGroup, errs.WrapParams{}, errs.String("group", spec.Group))
		}
		gid = g
	}
	//: the ids.
	return uid, gid, nil
}

// account is a resolved user: its uid and primary gid.
type account struct {
	uid, gid int
}

// lookupUser resolves a user name or numeric uid; a numeric uid with no
// entry is itself, its group left (-1).
func lookupUser(name string) (account, error) {
	//: a numeric uid.
	if n, nerr := strconv.Atoi(name); nerr == nil {
		u, lerr := user.LookupId(name)
		//: no entry: the uid alone.
		if lerr != nil {
			//: the uid, the group unchanged.
			return account{uid: n, gid: -1}, nil
		}
		//: the entry's.
		return accountOf(u)
	}
	u, err := user.Lookup(name)
	//: no such user.
	if err != nil {
		//: the lookup's error.
		return account{}, err
	}
	//: the entry's.
	return accountOf(u)
}

// accountOf reads a user entry's ids.
func accountOf(u *user.User) (account, error) {
	uid, err := strconv.Atoi(u.Uid)
	//: no numeric uid (Windows' SIDs).
	if err != nil {
		//: the parse's error.
		return account{}, err
	}
	gid, err := strconv.Atoi(u.Gid)
	//: no numeric gid.
	if err != nil {
		//: the parse's error.
		return account{}, err
	}
	//: both.
	return account{uid: uid, gid: gid}, nil
}

// lookupGroup resolves a group name or numeric gid.
func lookupGroup(name string) (int, error) {
	//: a numeric gid.
	if n, err := strconv.Atoi(name); err == nil {
		//: itself.
		return n, nil
	}
	g, err := user.LookupGroup(name)
	//: no such group.
	if err != nil {
		//: the lookup's error.
		return 0, err
	}
	//: its gid.
	return strconv.Atoi(g.Gid)
}
