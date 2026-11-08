package service

import (
	"context"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The replay harness drives the pure reducer with events only — no process,
// no clock, no goroutine — and plays the shell's part by hand: a spawn
// succeeds with the next PID (or fails when told), a timer fires when the
// harness's time passes its deadline, a reply is recorded. It is what ADR 0214
// §10 calls the replay scenario.

// sentSignal is one signal the reducer asked for.
type sentSignal struct {
	pid int
	sig coreproc.Signal
}

// harness is one unit's model and the shell's part, played by hand.
type harness struct {
	t         *testing.T
	now       time.Time
	m         model
	replies   map[uint64]error
	spawns    []effect
	signals   []sentSignal
	published []coresvc.StateValue
	forgotten []int
	attaches  []int
	timers    [timerCount]struct {
		at  time.Time
		seq uint64
		on  bool
	}
	// failSpawn makes the spawns of a list fail, with spawnErr when set.
	failSpawn map[list]bool
	spawnErr  error
	// adoptions are the scripted outcomes of the PID file's reads and the
	// guesses, in turn; past them a read is refused.
	adoptions []adoption
	// kept, dropped and closed are what the store kept, what it closed, and
	// the descriptors closed as nobody's.
	kept, dropped []storeEntry
	closed        []*os.File
	// fileReads and guesses count the shell's PID file reads and guesses;
	// picks are the guesses' pick flags.
	fileReads, guesses int
	// groupSignals are the group signals asked for (S0g); held is how many
	// processes the unit's group holds, which their outcome reports; prepares
	// and prunes count the spawns that prepared the group and Delegate='s
	// cleanups.
	groupSignals     []effect
	held             int
	prepares, prunes int
	// busOwned is whether BusName= has an owner, as the bus would say;
	// busWatched whether the shell's watch of it runs; busLookups how many
	// times a preparation looked it up (S0e).
	busOwned, busWatched bool
	busLookups           int
	picks                []bool
	nextPID              int
	nextID               uint64
	rand                 uint64
	mono                 uint64
	attachOK             bool
	exited               bool
}

// adoption is one scripted outcome of a PID file's read or a guess: pid
// adopted, or refused with reason; flags say what the cgroup listed.
type adoption struct {
	reason string
	pid    int
	flags  eventFlag
}

// stubNotify is a NotifySource the reducer tests never read.
type stubNotify struct{}

func (stubNotify) Socket() string { return "@test-notify" }
func (stubNotify) Recv(ctx context.Context) (coresvc.NotifyValue, error) {
	<-ctx.Done()
	return coresvc.NotifyValue{}, ctx.Err()
}
func (stubNotify) Close() error { return nil }

// stubCgroup is a CgroupKill the reducer tests never call.
type stubCgroup struct{}

func (stubCgroup) Kill() error                    { return nil }
func (stubCgroup) Signal(coreproc.Signal) error   { return nil }
func (stubCgroup) Contains(pid int) (bool, error) { return true, nil }

// stubMembers is a CgroupKill that lists its members, which the reducer
// tests never call.
type stubMembers struct{ stubCgroup }

func (stubMembers) Members() ([]int, error) { return nil, nil }

// stubAttacher is an Attacher the reducer tests never call.
type stubAttacher struct{}

func (stubAttacher) AttachPID(context.Context, int) (coreproc.Process, error) { return nil, nil }

func (stubAttacher) AttachPIDFile(context.Context, string) (coreproc.Process, error) { return nil, nil }

// cmd is a command for a test configuration.
func cmd(t *testing.T, words ...string) coresvc.CommandValue {
	t.Helper()
	c, err := coresvc.ParseCommand(words)
	if err != nil {
		t.Fatalf("ParseCommand(%q): %v", words, err)
	}
	return c
}

// newHarness resolves cfg and starts the replay at an arbitrary instant.
func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "unit"
	}
	s, err := resolve(&cfg)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return &harness{
		t: t, m: model{s: s}, now: time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC),
		replies: map[uint64]error{}, failSpawn: map[list]bool{}, nextPID: 100, mono: 1_000_000, attachOK: true,
	}
}

// feed runs one event and every event its effects hand back.
func (h *harness) feed(first event) {
	h.t.Helper()
	queue := []event{first}
	for len(queue) > 0 {
		ev := queue[0]
		queue = queue[1:]
		ev.at, ev.rand, ev.monotonic = h.now, h.rand, h.mono
		effects := reduce(&h.m, &ev)
		for _, ef := range effects {
			if next, ok := h.apply(ef); ok {
				queue = append(queue, next)
			}
		}
	}
}

// apply plays the shell's part for one effect.
func (h *harness) apply(ef effect) (event, bool) {
	switch ef.kind {
	case efSpawn:
		h.spawns = append(h.spawns, ef)
		var lookup eventFlag
		if ef.has(optPrepare) {
			h.prepares++
			lookup = h.lookup()
		}
		if h.failSpawn[ef.list] {
			err := h.spawnErr
			if err == nil {
				err = coresvc.ConfigInvalid
			}
			return event{kind: evSpawned, list: ef.list, spawnErr: err, flags: lookup}, true
		}
		h.nextPID++
		return event{kind: evSpawned, list: ef.list, pid: h.nextPID, flags: lookup}, true
	case efAttach:
		h.attaches = append(h.attaches, ef.pid)
		ev := event{kind: evAttached, pid: ef.pid}
		if h.attachOK {
			ev.flags = flagOK
		}
		return ev, true
	case efAttachFile, efGuess:
		return h.adopt(ef), true
	case efSignal:
		h.signals = append(h.signals, sentSignal{ef.pid, ef.sig})
	case efArm:
		h.timers[ef.timer].at, h.timers[ef.timer].seq, h.timers[ef.timer].on = ef.at, ef.seq, true
	case efDisarm:
		h.timers[ef.timer].on = false
	case efReply:
		for _, w := range ef.waiters {
			h.replies[w] = ef.err
		}
	case efForget:
		h.forgotten = append(h.forgotten, ef.pid)
	case efStore:
		h.kept = append(h.kept, ef.entries...)
	case efStoreDrop:
		h.dropped = append(h.dropped, ef.entries...)
	case efClose:
		h.closed = append(h.closed, ef.files...)
	case efPublish:
		h.published = append(h.published, ef.snap)
	case efSignalGroup:
		h.groupSignals = append(h.groupSignals, ef)
		return event{kind: evSignaled, sub: ef.sub, flags: h.population()}, true
	case efPrune:
		h.prunes++
	case efExit:
		h.exited = true
		for _, w := range ef.waiters {
			h.replies[w] = nil
		}
	}
	return event{}, false
}

// adopt plays a PID file's read or a guess from the script.
func (h *harness) adopt(ef effect) event {
	ev := event{kind: evAttached, flags: flagByFile}
	if ef.kind == efGuess {
		ev.flags = flagByGuess
		h.guesses++
		h.picks = append(h.picks, ef.has(optGuess))
	} else {
		h.fileReads++
	}
	if len(h.adoptions) == 0 {
		ev.reason = "PID_FILE_REFUSED"
		return ev
	}
	a := h.adoptions[0]
	h.adoptions = h.adoptions[1:]
	ev.pid, ev.reason, ev.flags = a.pid, a.reason, ev.flags|a.flags
	if a.pid != 0 {
		ev.flags |= flagOK
	}
	return ev
}

// lookup plays a start's preparation of a Type=dbus unit's name watch (S0e):
// the first one looks the owner up, a running watch does not again.
func (h *harness) lookup() eventFlag {
	if h.m.s.typ != coresvc.TypeDbus || h.busWatched {
		return 0
	}
	h.busWatched = true
	h.busLookups++
	if h.busOwned {
		return flagLookup | flagOwner
	}
	return flagLookup
}

// bus feeds a change of BusName='s owner the bus announced (S0e).
func (h *harness) bus(owned bool) {
	h.busOwned = owned
	ev := event{kind: evBusName}
	if owned {
		ev.flags = flagOwner
	}
	h.feed(ev)
}

// population is what the shell would read of the unit's group.
func (h *harness) population() eventFlag {
	if h.held > 0 {
		return flagPopulated
	}
	return flagEmpty
}

// group feeds a change of the unit's group (S0g): held processes left in it,
// and an OOM kill when oom.
func (h *harness) group(held int, oom bool) {
	h.held = held
	ev := event{kind: evCgroup, flags: h.population()}
	if oom {
		ev.flags |= flagOOM
	}
	h.feed(ev)
}

// endIn ends a process, read with the group holding held processes, with
// extra event flags (flagOOM) (S0g).
func (h *harness) endIn(pid int, exit coreproc.ExitValue, held int, extra eventFlag) {
	h.held = held
	h.feed(event{kind: evExit, pid: pid, exit: exit, flags: h.population() | extra})
}

// request posts a caller's request and returns its waiter.
func (h *harness) request(kind eventKind) uint64 {
	h.nextID++
	h.feed(event{kind: kind, waiter: h.nextID})
	return h.nextID
}

// start asks for a start and requires its waiter to be answered nil when
// wantDone, or still waiting otherwise.
func (h *harness) start() uint64 {
	return h.request(evStart)
}

// exit ends a process with a code.
func (h *harness) exit(pid, code int) {
	h.feed(event{kind: evExit, pid: pid, exit: coreproc.ExitValue{Code: code}})
}

// kill ends a process by a signal.
func (h *harness) kill(pid int, sig coreproc.Signal) {
	h.feed(event{kind: evExit, pid: pid, exit: coreproc.ExitValue{Code: -1, Signal: sig, Signaled: true}})
}

// notify sends a message from pid.
func (h *harness) notify(pid int, fields ...string) {
	f := map[string]string{}
	for _, kv := range fields {
		k, v, _ := strings.Cut(kv, "=")
		f[k] = v
	}
	ev := event{kind: evNotify, msg: coresvc.NotifyValue{Fields: f, PID: pid, UID: 1000, Verified: true}}
	if h.m.s.on(setHasCgroup) {
		ev.flags = flagMember | flagMainMember
	}
	h.feed(ev)
}

// advance moves the time by d, firing every timer that falls due, in order.
func (h *harness) advance(d time.Duration) {
	h.t.Helper()
	end := h.now.Add(d)
	for {
		k, at, ok := h.nextTimer(end)
		if !ok {
			break
		}
		h.now = at
		h.timers[k].on = false
		h.feed(event{kind: evTimer, timer: k, seq: h.timers[k].seq})
	}
	h.now = end
}

// nextTimer is the earliest armed timer due by end.
func (h *harness) nextTimer(end time.Time) (timerKind, time.Time, bool) {
	type due struct {
		k  timerKind
		at time.Time
	}
	var all []due
	for k := range timerCount {
		if h.timers[k].on && !h.timers[k].at.After(end) {
			all = append(all, due{k, h.timers[k].at})
		}
	}
	if len(all) == 0 {
		return 0, time.Time{}, false
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })
	return all[0].k, all[0].at, true
}

// deadline is how long until timer k fires, failing when it is not armed.
func (h *harness) deadline(k timerKind) time.Duration {
	h.t.Helper()
	if !h.timers[k].on {
		h.t.Fatalf("timer %d is not armed (sub %s)", k, h.m.sub)
	}
	return h.timers[k].at.Sub(h.now)
}

// lastSpawn is the last spawn the reducer asked for.
func (h *harness) lastSpawn() effect {
	h.t.Helper()
	if len(h.spawns) == 0 {
		h.t.Fatal("nothing was spawned")
	}
	return h.spawns[len(h.spawns)-1]
}

// spawnsOf lists the spawns of a list.
func (h *harness) spawnsOf(l list) []effect {
	var out []effect
	for _, ef := range h.spawns {
		if ef.list == l {
			out = append(out, ef)
		}
	}
	return out
}

// sentTo lists the signals sent to pid.
func (h *harness) sentTo(pid int) []coreproc.Signal {
	var out []coreproc.Signal
	for _, s := range h.signals {
		if s.pid == pid {
			out = append(out, s.sig)
		}
	}
	return out
}

// want requires the unit at a state and a step.
func (h *harness) want(state coresvc.State, sub coresvc.Sub) {
	h.t.Helper()
	if h.m.state != state || h.m.sub != sub {
		h.t.Fatalf("unit at %s/%s, want %s/%s (result %s)", h.m.state, h.m.sub, state, sub, h.m.result)
	}
}

// wantReply requires a waiter answered, with an error carrying code (0: nil).
func (h *harness) wantReply(id uint64, code errs.Code) {
	h.t.Helper()
	err, ok := h.replies[id]
	if !ok {
		h.t.Fatalf("waiter %d not answered (unit at %s/%s)", id, h.m.state, h.m.sub)
	}
	if code == 0 && err != nil {
		h.t.Fatalf("waiter %d: %v, want nil", id, err)
	}
	if code != 0 && !errs.HasCode(err, code) {
		h.t.Fatalf("waiter %d: %v, want code %x", id, err, code)
	}
}

// wantWaiting requires a waiter not answered yet.
func (h *harness) wantWaiting(id uint64) {
	h.t.Helper()
	if err, ok := h.replies[id]; ok {
		h.t.Fatalf("waiter %d answered %v, want waiting", id, err)
	}
}

// hasVar reports whether a spawn's variables hold assignment.
func hasVar(ef effect, assignment string) bool {
	return slices.Contains(ef.vars, assignment)
}

// publishedStates is the sequence of states published.
func (h *harness) publishedStates() []coresvc.State {
	var out []coresvc.State
	for _, s := range h.published {
		if len(out) == 0 || out[len(out)-1] != s.State {
			out = append(out, s.State)
		}
	}
	return out
}

// stopAndExit asks for a stop and ends the main process on its signal.
func (h *harness) stopAndExit() uint64 {
	main := h.m.mainPID
	id := h.request(evStop)
	if main != 0 {
		h.kill(main, sigTERM)
	}
	return id
}

// hasCode reports whether err carries code.
func hasCode(err error, code errs.Code) bool {
	return errs.HasCode(err, code)
}
