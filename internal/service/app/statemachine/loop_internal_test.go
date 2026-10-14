// Package statemachine — white-box tests of the loop's arming against a clock
// that moves while it arms.
package statemachine

import (
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// overtakingClock is a manual clock that moves to target as a timer is armed,
// after the duration was read and before the timer starts counting: what a
// test's Set does when it lands there, and what a descheduled goroutine does
// on the system clock (#304: TestAWriteThatBringsADeadlineForwardWakesTheLoop
// slept through its deadline on macOS).
type overtakingClock struct {
	*clock.ManualClock
	target time.Time
}

// NewTimer moves the clock to target, then arms the timer for d from there.
func (c *overtakingClock) NewTimer(d time.Duration) clock.Timer {
	c.Set(c.target)
	return c.ManualClock.NewTimer(d)
}

// TestATargetTheClockPassesWhileArmingIsDueAtOnce pins arm against that
// interleaving: the duration it read would count again from the new instant,
// so the wait would end that much after the target — never, on a manual
// clock nobody moves again. A target the clock has reached once the timer is
// armed is due at once; one it has not reached is not.
func TestATargetTheClockPassesWhileArmingIsDueAtOnce(t *testing.T) {
	t.Parallel()
	at := t0.Add(20 * time.Minute)
	type tc struct {
		name string
		// moveTo is where the clock moves while the loop arms.
		moveTo time.Time
		// wantDue says the wait must be due once arm returns.
		wantDue bool
	}
	cases := []tc{
		{name: "the clock reaches the target", moveTo: at, wantDue: true},
		{name: "the clock passes the target", moveTo: at.Add(time.Minute), wantDue: true},
		{name: "the clock stops short of the target", moveTo: at.Add(-time.Minute), wantDue: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := &StateMachine[struct{}, int]{cfg: settings[struct{}, int]{
			clock: &overtakingClock{ManualClock: clock.NewManualClock(t0), target: c.moveTo},
		}}
		timer, fire := m.arm(at)
		defer stopTimer(timer)
		select {
		//: due: never before its target.
		case got := <-fire:
			//: a wait that ends early is as wrong as one that never ends.
			if !c.wantDue || got.Before(at) {
				t.Errorf("the wait ended at %v, before its target %v", got, at)
			}
		//: not due yet.
		default:
			//: the clock reached the target while the loop armed.
			if c.wantDue {
				t.Fatalf("the clock moved to %v while the loop armed for %v, and the wait is not due", c.moveTo, at)
			}
		}
	}
	//: each case on its own.
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
