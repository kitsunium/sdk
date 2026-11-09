package service

import (
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// Type=dbus (S0e, kitsunium/sdk#737): the unit is up once its main process
// runs and its BusName= is owned, as service.c's service_bus_name_owner_change
// and service_good read the bus. The shell watches the name (actor_dbus.go);
// the reducer reads the owner the bus announced, never a guess.

// busNameGrace is how long a Type=dbus unit alive without its name is given
// to own it again: systemd's SERVICE_BUS_NAME_GRACE_USEC.
const busNameGrace time.Duration = 2 * time.Second

// onBusName takes BusName='s owner (service_bus_name_owner_change):
// starting, an owner makes the unit ready; running, a loss the bus announced
// stops it at once — NameOwnerChanged is definitive —, while a lookup that
// finds no owner — flagLookup: the first answer of a watch begun while the
// unit runs, S0h's restored unit's — leaves it the grace (from_signal false);
// an owner while it awaits its name brings it back. A watch that ended
// changes nothing: the level stays what the bus last said, as systemd keeps
// it while it is disconnected.
func (st *step) onBusName() {
	m := st.m
	ev := st.ev
	//: the watch ended: the last owner stands.
	if ev.has(flagUnwatched) {
		return
	}
	owned := ev.has(flagOwner)
	m.set(flagBusOwned, owned)
	//: only a Type=dbus unit is up on its name.
	if m.s.typ != coresvc.TypeDbus {
		return
	}
	//: by the step.
	switch m.sub {
	//: up, or awaiting its name.
	case coresvc.SubRunning, coresvc.SubRunningRevalidating:
		//: the bus announced the loss: no grace.
		if !owned && !ev.has(flagLookup) {
			st.exitedOrStop()
			return
		}
		st.enterRunning(coresvc.ResultSuccess)
	//: the main process runs and the name is now owned: ready.
	case coresvc.SubStart:
		//: owned.
		if owned {
			st.enterStartPost()
		}
	//: any other step: the level alone, read when the unit comes up.
	default:
	}
}

// goodExceptBus is service.c's service_good_except_bus_name: a main process,
// or a cgroup with members and no main process found.
func (st *step) goodExceptBus() bool {
	m := st.m
	//: a process to be up on.
	return m.mainPID != 0 || m.has(flagCgroupGood)
}

// good is service.c's service_good: something to be up on, and a Type=dbus
// unit's name owned.
func (st *step) good() bool {
	m := st.m
	//: the name, for Type=dbus, and a process.
	return (m.s.typ != coresvc.TypeDbus || m.has(flagBusOwned)) && st.goodExceptBus()
}

// awaitsBusName reports a Type=dbus unit alive whose name has no owner.
func (st *step) awaitsBusName() bool {
	m := st.m
	//: alive, its name not owned.
	return m.s.typ == coresvc.TypeDbus && !m.has(flagBusOwned) && st.goodExceptBus()
}

// enterRevalidating gives a Type=dbus unit alive without its name a grace to
// own it again (systemd's SERVICE_RUNNING_REVALIDATING): an active state
// whose timer is the grace — RuntimeMaxSec='s waits, the time spent here not
// counted when the unit was running. A unit awaiting its name already keeps
// the grace it was given on the way in.
func (st *step) enterRevalidating() {
	m := st.m
	//: awaiting already: the deadline stands.
	if m.sub == coresvc.SubRunningRevalidating {
		return
	}
	//: from running: the run's time stops here.
	if m.sub == coresvc.SubRunning {
		m.revalidateBegin = st.ev.at
	}
	st.runtimeDeadline()
	st.arm(timerState, busNameGrace)
	st.setState(coresvc.StateActive, coresvc.SubRunningRevalidating)
}

// resumeRuntime moves RuntimeMaxSec='s end past the time a unit that was
// running spent awaiting its name, as systemd re-arms the budget left.
func (st *step) resumeRuntime() {
	m := st.m
	//: not back from a revalidation that began running.
	if m.revalidateBegin.IsZero() {
		return
	}
	//: a run with an end.
	if !m.runtimeAt.IsZero() {
		m.runtimeAt = m.runtimeAt.Add(st.ev.at.Sub(m.revalidateBegin))
	}
	m.revalidateBegin = time.Time{}
}
