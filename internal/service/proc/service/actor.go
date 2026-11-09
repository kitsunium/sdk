package service

import (
	"math/rand/v2"
	"os"
	"strconv"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/systemd/listen"
)

const (
	// shellWatching is set while the group's watch runs (S0g).
	shellWatching shellFlag = 1 << iota
	// shellBusWatching is set while the bus name's watch runs (S0e).
	shellBusWatching
	// shellExiting is set once the reducer asked to end.
	shellExiting
)

// appliers do each effect, by kind; one whose outcome the reducer must know
// returns it as an event, and true.
var appliers = [...]func(a *actor, ef *effect) (event, bool){
	efSpawn:      func(a *actor, ef *effect) (event, bool) { return a.spawnCommand(ef), true },
	efAttach:     func(a *actor, ef *effect) (event, bool) { return a.attach(ef.pid), true },
	efAttachFile: func(a *actor, _ *effect) (event, bool) { return a.attachFile(), true },
	efGuess:      func(a *actor, ef *effect) (event, bool) { return a.guessMain(ef.has(optGuess)), true },
	efSignal:     (*actor).signal,
	efArm:        (*actor).arm,
	efDisarm:     func(a *actor, ef *effect) (event, bool) { a.stopTimer(ef.timer); return event{}, false },
	efReply:      func(a *actor, ef *effect) (event, bool) { a.u.answer(ef.waiters, ef.err); return event{}, false },
	efClose:      func(_ *actor, ef *effect) (event, bool) { closeFiles(ef.files); return event{}, false },
	efStore:      func(a *actor, ef *effect) (event, bool) { a.keep(ef); return event{}, false },
	efStoreDrop:  func(a *actor, ef *effect) (event, bool) { a.drop(ef.entries); return event{}, false },
	efForget:     func(a *actor, ef *effect) (event, bool) { delete(a.procs, ef.pid); return event{}, false },
	efPublish: func(a *actor, ef *effect) (event, bool) {
		a.u.publish(&ef.snap, ef.prevState, ef.prevSub)
		return event{}, false
	},
	efExit:        (*actor).end,
	efSignalGroup: func(a *actor, ef *effect) (event, bool) { return a.signalGroup(ef), true },
	efPrune:       func(a *actor, _ *effect) (event, bool) { a.prune(); return event{}, false },
}

// actor is the I/O shell of one unit (ADR 0214 §3): it owns the model, the
// processes and the timers, takes one event at a time, runs the reducer, and
// does what the reducer asks. Only the actor's goroutine touches it.
type actor struct {
	u     *Unit
	procs map[int]coreproc.Process
	// children are the processes the engine spawned: the image's children,
	// reclaimed as such across a hand-over (S0h).
	children map[int]bool
	// store holds the descriptor store's descriptors by id, storeOrder their
	// ids oldest first; hangups watches them, once one is.
	store      map[uint64]*held
	storeOrder []uint64
	hangups    *hangupWatch
	timers     [timerCount]clock.Timer
	armed      [timerCount]uint64
	m          model
	// group is the unit's control group when it runs the cgroup modes (S0g),
	// nil otherwise; oomSeen is the OOM kills it counted that the reducer
	// was told of.
	group   coresvc.CgroupUnit
	oomSeen int64
	// flags are the shell's booleans: the group's watch, the bus name's,
	// the end asked.
	flags shellFlag
}

// shellFlag is one of the shell's booleans, as a bit.
type shellFlag uint8

// on reports whether flag f is set.
func (a *actor) on(f shellFlag) bool {
	//: the bit.
	return a.flags&f != 0
}

// put sets or clears flag f.
func (a *actor) put(f shellFlag, on bool) {
	//: set.
	if on {
		a.flags |= f
		return
	}
	a.flags &^= f
}

// run is the actor's goroutine.
func (u *Unit) run(s *settings) {
	a := &actor{u: u, m: model{s: s}, procs: map[int]coreproc.Process{}, children: map[int]bool{}, store: map[uint64]*held{}}
	//: the group the cgroup modes run on.
	if s.on(setGroupUnit) {
		a.group, _ = u.cfg.Cgroup.(coresvc.CgroupUnit)
	}
	//: Close returns once the end is complete: the store closed included.
	defer u.readers.Done()
	defer a.finish()
	//: a unit resumed from a hand-over (S0h).
	if r := u.restoring; r != nil {
		u.restoring = nil
		a.restore(r)
		r.ready <- struct{}{}
	}
	//: until the reducer asks to end.
	for !a.on(shellExiting) {
		a.wait()
	}
}

// wait takes the next event, or a timer's fire.
func (a *actor) wait() {
	var fires [timerCount]<-chan time.Time
	//: each armed timer's channel; a nil one blocks forever.
	for k := range timerCount {
		//: armed.
		if a.timers[k] != nil {
			fires[k] = a.timers[k].C()
		}
	}
	//: an event, or a timer's fire.
	select {
	//: an event; a freeze is the shell's (S0h).
	case ev := <-a.u.inbox:
		//: a freeze.
		if ev.kind == evFreeze {
			a.freeze(&ev)
			return
		}
		a.handle(&ev)
	//: the step's timer.
	case <-fires[timerState]:
		a.timerFired(timerState)
	//: the keep-alive deadline.
	case <-fires[timerWatchdog]:
		a.timerFired(timerWatchdog)
	//: a forking unit's next read of its PID file.
	case <-fires[timerPIDFile]:
		a.timerFired(timerPIDFile)
	}
}

// timerFired posts timer k's fire with the arming it fired for.
func (a *actor) timerFired(k timerKind) {
	a.timers[k] = nil
	a.handle(&event{kind: evTimer, timer: k, seq: a.armed[k]})
}

// handle runs the reducer over ev and every event its effects hand back,
// in order, before the next event of the inbox.
func (a *actor) handle(first *event) {
	queue := []event{*first}
	//: until no effect hands an event back.
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]
		ev.at, ev.rand, ev.monotonic = a.u.clock.Now(), rand.Uint64(), monotonicMicros()
		a.annotate(&ev)
		effects := reduce(&a.m, &ev)
		//: each effect in order; an outcome joins the queue.
		for i := range effects {
			//: an effect whose outcome the reducer must know.
			if next, ok := appliers[effects[i].kind](a, &effects[i]); ok {
				queue = append(queue, next)
			}
		}
		//: an exited process is no longer the shell's.
		if ev.kind == evExit {
			delete(a.procs, ev.pid)
			delete(a.children, ev.pid)
		}
	}
}

// annotate finds what the reducer cannot read: what the unit's group holds
// now, with a process's end or a change the watch announced (S0g); which of
// a FDSTORE=1 message's descriptors the store holds already (fstat), and
// whether a notification's sender, and the MAINPID= it names, belong to the
// unit's cgroup — one that cannot answer is read as "no".
func (a *actor) annotate(ev *event) {
	//: by the event.
	switch ev.kind {
	//: a process's end, or the group's change: the group read now.
	case evExit, evCgroup:
		a.annotateGroup(ev)
	//: the bus name's watch ended: started again at the next start (S0e).
	case evBusName:
		a.put(shellBusWatching, a.on(shellBusWatching) && !ev.has(flagUnwatched))
	//: a notification.
	case evNotify:
		a.annotateNotify(ev)
	//: nothing to read.
	default:
	}
}

// annotateNotify finds which of a FDSTORE=1 message's descriptors the store
// holds already, and whether the sender and the MAINPID= it names belong to
// the unit's cgroup.
func (a *actor) annotateNotify(ev *event) {
	//: descriptors to store.
	if a.u.s.storeMax > 0 && ev.msg.Fields["FDSTORE"] == "1" && len(ev.msg.Files) > 0 {
		ev.dupOf = a.dupsOf(ev.msg.Files)
	}
	cg := a.u.cfg.Cgroup
	//: no cgroup to ask.
	if cg == nil {
		return
	}
	//: the sender.
	if belongs(cg, ev.msg.PID) {
		ev.flags |= flagMember
	}
	pid, err := strconv.Atoi(ev.msg.Fields["MAINPID"])
	//: a MAINPID= that belongs.
	if err == nil && belongs(cg, pid) {
		ev.flags |= flagMainMember
	}
}

// belongs reports whether pid is a process of the cgroup; an error is "no".
func belongs(cg coresvc.CgroupKill, pid int) bool {
	//: a process.
	if pid <= 0 {
		//: no.
		return false
	}
	member, err := cg.Contains(pid)
	//: a yes the cgroup could give.
	return err == nil && member
}

// signal sends a signal to a process the shell holds. A refusal means the
// process is gone already, which its exit event says: nothing to do.
func (a *actor) signal(ef *effect) (event, bool) {
	//: a process the shell holds.
	if p := a.procs[ef.pid]; p != nil {
		gone(p.Signal(ef.sig))
	}
	//: no outcome.
	return event{}, false
}

// arm arms a timer for its deadline, replacing its last arming.
func (a *actor) arm(ef *effect) (event, bool) {
	a.stopTimer(ef.timer)
	a.timers[ef.timer] = a.u.clock.NewTimer(ef.at.Sub(a.u.clock.Now()))
	a.armed[ef.timer] = ef.seq
	//: no outcome.
	return event{}, false
}

// end answers the close's waiters and ends the loop.
func (a *actor) end(ef *effect) (event, bool) {
	a.flags |= shellExiting
	a.u.answer(ef.waiters, nil)
	//: no outcome.
	return event{}, false
}

// spawnCommand spawns a command and watches its process. A start's first
// spawn prepares the unit's group (S0g) and its bus name's watch (S0e) first;
// what the watch looked up rides on the spawn's outcome, whatever it is.
func (a *actor) spawnCommand(ef *effect) event {
	ev := event{kind: evSpawned, list: ef.list}
	//: a start's first spawn.
	if ef.has(optPrepare) {
		lookup, err := a.prepareStart()
		ev.flags = lookup
		//: a group that cannot be watched, delegated or set, a name that
		//: cannot be watched: the spawn's failure.
		if err != nil {
			ev.spawnErr = err
			//: the failure, for the reducer.
			return ev
		}
	}
	spec := buildSpec(&a.u.cfg.Spec, a.u.s, ef)
	//: what a main command is owed: a connection (S0f), the store's (S0d).
	if err := a.handOver(&spec, a.owed(ef)); err != nil {
		ev.spawnErr = err
		//: a hand-over refused: the spawn's failure.
		return ev
	}
	p, err := a.u.spawn.Spawn(a.u.ctx, spec)
	//: refused or failed.
	if err != nil {
		ev.spawnErr = err
		//: the failure, for the reducer.
		return ev
	}
	a.watch(p)
	ev.pid = p.PID()
	a.children[ev.pid] = true
	//: the process, for the reducer.
	return ev
}

// owed is what a spawn is handed beyond its spec: for ExecStart= alone, an
// instance's connection (S0f), then the store's descriptors the reducer listed
// (S0d) — systemd's order, the socket before the store.
func (a *actor) owed(ef *effect) []listen.NamedFileValue {
	//: a command.
	if ef.list != listStart {
		//: nothing.
		return nil
	}
	stored := a.stored(ef.entries)
	//: no connection.
	if a.u.conn == nil {
		//: the store's.
		return stored
	}
	//: the connection, then the store's.
	return append([]listen.NamedFileValue{{Name: connectionName, File: a.u.conn}}, stored...)
}

// attach adopts a process a MAINPID= named and watches it.
func (a *actor) attach(pid int) event {
	p, err := a.u.cfg.Attacher.AttachPID(a.u.ctx, pid)
	//: refused.
	if err != nil {
		//: the refusal, for the reducer.
		return event{kind: evAttached, pid: pid}
	}
	a.watch(p)
	//: adopted.
	return event{kind: evAttached, pid: p.PID(), flags: flagOK}
}

// attachFile adopts the process PIDFile= names and watches it. A refusal —
// the file absent, unsafe or naming no process the scope admits — carries
// its reason, and whether the cgroup has anything left to write the file.
func (a *actor) attachFile() event {
	ev := event{kind: evAttached, flags: flagByFile}
	p, err := a.u.cfg.Attacher.AttachPIDFile(a.u.ctx, a.u.s.pidFile)
	//: refused, or not written yet.
	if err != nil {
		ev.reason = reasonOf(err)
		_, population := a.members()
		ev.flags |= population
		//: the refusal, for the reducer.
		return ev
	}
	a.watch(p)
	ev.pid = p.PID()
	ev.flags |= flagOK
	//: adopted.
	return ev
}

// guessMain lists the unit's cgroup and, when pick is set, adopts the one
// member whose parent is not a member (GuessMainPID=). The event says whether
// the cgroup has members, so a main process left unknown is up while it has.
func (a *actor) guessMain(pick bool) event {
	ev := event{kind: evAttached, flags: flagByGuess}
	members, population := a.members()
	ev.flags |= population
	//: no guess asked, or nobody to guess from.
	if !pick || population != flagPopulated {
		//: the population alone.
		return ev
	}
	root, found := soleRoot(members, a.u.s.selfPID)
	//: none, or several: the main process stays unknown.
	if !found {
		//: the population alone.
		return ev
	}
	p, err := a.u.cfg.Attacher.AttachPID(a.u.ctx, root)
	//: gone meanwhile, or refused.
	if err != nil {
		ev.reason = reasonOf(err)
		//: the population alone.
		return ev
	}
	a.watch(p)
	ev.pid = p.PID()
	ev.flags |= flagOK
	//: adopted.
	return ev
}

// members lists the unit's cgroup when it lists its members, with
// flagPopulated or flagEmpty; nothing when it cannot say.
func (a *actor) members() ([]int, eventFlag) {
	lister, ok := a.u.cfg.Cgroup.(coresvc.CgroupMembers)
	//: no cgroup, or one that does not list.
	if !ok {
		//: unknown.
		return nil, 0
	}
	pids, err := lister.Members()
	//: the listing failed: unknown, never "empty".
	if err != nil {
		//: unknown.
		return nil, 0
	}
	//: nobody left.
	if len(pids) == 0 {
		//: empty.
		return nil, flagEmpty
	}
	//: the members.
	return pids, flagPopulated
}

// reasonOf is an error's reason ("PID_FILE_REFUSED"), empty when it has none.
func reasonOf(err error) string {
	reason, _ := errs.ReasonOf(err)
	//: the reason, or nothing.
	return reason
}

// watch holds a process and posts its end.
//
// Goroutine lifecycle: one per process, ended by the process's end. A process
// the unit leaves running (KillMode=none, SurviveFinalKillSignal=) keeps its
// goroutine until it ends, and its end is dropped once the unit is closed.
func (a *actor) watch(p coreproc.Process) {
	u := a.u
	a.procs[p.PID()] = p
	//: Goroutine lifecycle: ended by the process's end (see above); its post
	//: is taken by the actor or dropped once done is closed.
	go func() {
		ev := waitEvent(p)
		//: taken, or the unit ended.
		select {
		//: taken.
		case u.inbox <- ev:
		//: nobody to take it.
		case <-u.done:
		}
	}()
}

// waitEvent waits for a process's end and makes it an event.
func waitEvent(p coreproc.Process) event {
	exit, err := p.Wait()
	ev := event{kind: evExit, pid: p.PID(), exit: exit}
	//: an adopted process gone without a status: the port's EXIT_UNKNOWN, or
	//: proc/exec's STATUS_UNKNOWN from an attached process (NewAttacher).
	if errs.HasCode(err, coresvc.CodeExitUnknown) || errs.HasCode(err, coreproc.CodeStatusUnknown) {
		ev.flags |= flagUnknown
	}
	//: a wait that failed: an end, unclean.
	if errs.HasCode(err, coreproc.CodeWaitFailed) {
		ev.exit = coreproc.ExitValue{Code: -1}
	}
	//: the end.
	return ev
}

// stopTimer stops timer k, if armed.
func (a *actor) stopTimer(k timerKind) {
	//: armed.
	if a.timers[k] != nil {
		a.timers[k].Stop()
		a.timers[k] = nil
	}
}

// finish ends the unit: the timers stopped, the context canceled, the socket
// closed, the store closed — its watch stopped once nothing posts —, an
// instance's connection closed, every subscription closed.
func (a *actor) finish() {
	u := a.u
	//: each timer.
	for k := range timerCount {
		a.stopTimer(k)
	}
	u.cancel()
	//: the notification socket, whose closing error nobody could act on.
	if u.cfg.Notify != nil {
		gone(u.cfg.Notify.Close())
	}
	u.doneOnce.Do(func() { close(u.done) })
	a.closeStore()
	dropPosted(u.inbox)
	//: an instance's connection ends with it (S0f).
	if u.conn != nil {
		gone(u.conn.Close())
	}
	u.closeSubscriptions()
}

// dropPosted closes the descriptors of the notifications posted before the
// actor ended, which nobody takes now.
func dropPosted(inbox chan event) {
	//: what is buffered.
	for n := len(inbox); n > 0; n-- {
		ev := <-inbox
		closeFiles(ev.msg.Files)
	}
}

// closeFiles closes descriptors nobody keeps; a descriptor that fails to
// close is gone all the same.
func closeFiles(files []*os.File) {
	//: each descriptor.
	for _, f := range files {
		//: a descriptor.
		if f != nil {
			gone(f.Close())
		}
	}
}

// gone takes an error the engine cannot act on: a signal to a process that
// ended, a descriptor or a socket that is closed whatever the close says.
func gone(err error) {
	//: read, so the discard is deliberate.
	if err == nil {
		return
	}
	//: the process ended, or the descriptor is released: nothing left to do.
}
