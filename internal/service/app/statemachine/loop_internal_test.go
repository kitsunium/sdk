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
// armed is due at once.
func TestATargetTheClockPassesWhileArmingIsDueAtOnce(t *testing.T) {
	t.Parallel()
	at := t0.Add(20 * time.Minute)
	m := &StateMachine[struct{}, int]{cfg: settings[struct{}, int]{
		clock: &overtakingClock{ManualClock: clock.NewManualClock(t0), target: at},
	}}
	timer, fire := m.arm(at)
	defer stopTimer(timer)
	select {
	case got := <-fire:
		if got.Before(at) {
			t.Errorf("the wait ended at %v, before its target %v", got, at)
		}
	default:
		t.Fatal("the clock reached the target while the loop armed, and the wait is not due")
	}
}
