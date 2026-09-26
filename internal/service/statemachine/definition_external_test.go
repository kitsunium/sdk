// Package statemachine_test — the declaration: what it refuses, and what a
// caller reads back from it to draw the machine.
package statemachine_test

import (
	"context"
	"slices"
	"testing"
	"time"

	corestm "github.com/kitsunium/sdk/internal/core/statemachine"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcstm "github.com/kitsunium/sdk/internal/service/statemachine"
)

// lifecycle is the shop's machine, as kit's own test product declares it.
func lifecycle() *svcstm.MachineSpec[Item, State] {
	return svcstm.NewMachineSpec(stateOf).
		Initial(Draft).
		On("publish", Draft, Live).
		On("sell", Live, Sold).
		After("expire", time.Hour, Live, Expired).
		At("lapse", Live, Expired, expiry).
		When("sold-out", Live, Sold, soldOut).
		On("retire", Draft, Retired)
}

// soldOut holds when nothing is left.
func soldOut(i Item) bool { return i.Stock == 0 }

// expiry is the item's own expiry date, when it has one.
func expiry(i Item) (time.Time, bool) {
	if i.Expires == nil {
		return time.Time{}, false
	}
	return *i.Expires, true
}

// TestTheDeclarationReadsBack pins what a caller drawing the machine gets: the
// states in order of first mention, the transitions in declaration order with
// their triggers, the initial state, and Can.
func TestTheDeclarationReadsBack(t *testing.T) {
	t.Parallel()
	def := lifecycle()
	if states := def.States(); !slices.Equal(states, []State{Draft, Live, Sold, Expired, Retired}) {
		t.Errorf("States() = %v", states)
	}
	got := def.Transitions()
	want := []svcstm.TransitionValue[State]{
		{Event: "publish", From: Draft, To: Live, Trigger: corestm.TriggerEvent},
		{Event: "sell", From: Live, To: Sold, Trigger: corestm.TriggerEvent},
		{Event: "expire", From: Live, To: Expired, Trigger: corestm.TriggerDelay, Delay: time.Hour},
		{Event: "lapse", From: Live, To: Expired, Trigger: corestm.TriggerDeadline},
		{Event: "sold-out", From: Live, To: Sold, Trigger: corestm.TriggerGuard},
		{Event: "retire", From: Draft, To: Retired, Trigger: corestm.TriggerEvent},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Transitions() =\n%v\nwant\n%v", got, want)
	}
	if initial, set := def.InitialState(); !set || initial != Draft {
		t.Errorf("InitialState() = %q, %v", initial, set)
	}
	if !def.Can(Item{State: Draft}, "publish") || def.Can(Item{State: Sold}, "publish") || def.Can(Item{State: Live}, "expire") {
		t.Error("Can disagrees with the declared event transitions")
	}
	if len(def.Problems()) != 0 {
		t.Errorf("a valid declaration has problems: %v", def.Problems())
	}
}

// TestEveryDeclarationMistakeIsRefusedByCode walks each mistake a declaration
// can make and checks NewStateMachine names it by its code.
func TestEveryDeclarationMistakeIsRefusedByCode(t *testing.T) {
	t.Parallel()
	nop := func(context.Context, *Item) error { return nil }
	type tc struct {
		name    string
		def     func() *svcstm.MachineSpec[Item, State]
		want    errs.Code
		storeOK bool
	}
	cases := []tc{
		{"no initial state", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).On("go", Draft, Live)
		}, svcstm.CodeInitialMissing, true},
		{"an empty event name", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).On("", Draft, Live)
		}, svcstm.CodeEventInvalid, true},
		{"the creation's name", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).On(corestm.CreateEvent, Draft, Live)
		}, svcstm.CodeEventInvalid, true},
		{"one event twice from one state", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).On("go", Draft, Live).After("go", time.Minute, Draft, Sold)
		}, svcstm.CodeTransitionDuplicate, true},
		{"a zero delay", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).After("tick", 0, Draft, Live)
		}, svcstm.CodeDelayInvalid, true},
		{"a nil instant", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).At("due", Draft, Live, nil)
		}, svcstm.CodeFunctionMissing, true},
		{"a nil guard", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).When("ready", Draft, Live, nil)
		}, svcstm.CodeFunctionMissing, true},
		{"a nil OnEnter hook", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).OnEnter(Live, nil)
		}, svcstm.CodeFunctionMissing, true},
		{"a nil OnTransition hook", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).OnTransition(nil)
		}, svcstm.CodeFunctionMissing, true},
		{"a nil state accessor", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec[Item, State](nil).Initial(Draft)
		}, svcstm.CodeFunctionMissing, true},
		{"a nil definition", func() *svcstm.MachineSpec[Item, State] { return nil }, svcstm.CodeFunctionMissing, true},
		{"no store", func() *svcstm.MachineSpec[Item, State] {
			return svcstm.NewMachineSpec(stateOf).Initial(Draft).OnEnter(Live, nop)
		}, svcstm.CodeStoreMissing, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := &svcstm.Config[Item, State]{}
		if c.storeOK {
			cfg.Store = newMemStore()
		}
		m, err := svcstm.NewStateMachine(t.Context(), c.def(), cfg)
		if m != nil || !errs.HasCode(err, c.want) {
			t.Fatalf("NewStateMachine() = %v, %v; want the code %s", m, err, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestProblemsAreAllReportedAtOnce pins that one NewStateMachine lists every
// mistake, so one run of a program shows them all, and that Problems keeps
// them in declaration order for a caller that attributes each to a line.
func TestProblemsAreAllReportedAtOnce(t *testing.T) {
	t.Parallel()
	def := svcstm.NewMachineSpec(stateOf).On("", Draft, Live).After("tick", -time.Second, Draft, Live)
	problems := def.Problems()
	if len(problems) != 2 || !errs.HasCode(problems[0], svcstm.CodeEventInvalid) || !errs.HasCode(problems[1], svcstm.CodeDelayInvalid) {
		t.Fatalf("Problems() = %v", problems)
	}
	_, err := svcstm.NewStateMachine(t.Context(), def, &svcstm.Config[Item, State]{})
	for _, code := range []errs.Code{svcstm.CodeEventInvalid, svcstm.CodeDelayInvalid, svcstm.CodeInitialMissing, svcstm.CodeStoreMissing} {
		if !errs.HasCode(err, code) {
			t.Errorf("NewStateMachine() = %v, missing %s", err, code)
		}
	}
}

// TestAMachineKeepsItsOwnCopyOfTheDeclaration pins that a declaration going on
// after NewStateMachine reaches no machine.
func TestAMachineKeepsItsOwnCopyOfTheDeclaration(t *testing.T) {
	t.Parallel()
	def := svcstm.NewMachineSpec(stateOf).Initial(Draft)
	m := open(t, def, &svcstm.Config[Item, State]{Store: newMemStore()})
	def.On("publish", Draft, Live)
	if _, err := m.Start(t.Context(), Item{ID: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fire(t.Context(), "a", "publish"); !errs.HasCode(err, svcstm.CodeTransitionRefused) {
		t.Fatalf("a transition declared after NewStateMachine fired: %v", err)
	}
}
