// Package sql — hosts the liveness probe and the budget that bounds it.
package sql

import (
	"context"

	coresql "github.com/kitsunium/sdk/internal/core/sql"
)

// checker is the concrete core/sql.Checker. It stays unexported behind
// [NewChecker] (IFACE-PLUGIN).
type checker struct {
	// cfg is the validated, clamped configuration.
	cfg resolved
}

// NewChecker returns a bounded liveness probe over cfg.DB.
//
// Like [NewTransactor] it applies cfg.Pool, so a process whose only SQL port
// is a health check still gets its pool policy installed.
func NewChecker(cfg Config) (probe coresql.Checker, err error) {
	res, err := cfg.resolve()
	//: a refused Config leaves the caller's DB exactly as it was.
	if err != nil {
		//: propagate CONFIG_INVALID / POOL_MISCONFIGURED unchanged.
		return nil, err
	}
	//: the probe owns nothing but its configuration.
	return &checker{cfg: res}, nil
}

// Check pings the database under [Config.CheckTimeout].
//
// The budget is armed on the injected clock rather than with
// context.WithTimeout, so a test asserts the timeout by advancing a
// ManualClock instead of sleeping through a real one. What an expired budget
// does is deliberately the same three things ADR 0050 fixed on lifecycle's
// shutdown: it CANCELS the probe's context — an announcement the driver acts
// on — it stops WAITING, and it reports. It kills no goroutine, because Go
// cannot, and it closes nothing the pool owns.
func (c *checker) Check(ctx context.Context) error {
	//: the probe's own context, cancelled by the budget or by this return.
	probe, cancel := context.WithCancel(ctx)
	defer cancel()
	//: armed BEFORE the work starts, so "when does the budget begin" has an
	//: answer that does not depend on goroutine scheduling.
	timer := c.cfg.clk.NewTimer(c.cfg.probe)
	defer timer.Stop()
	//: buffered by one: an abandoned ping's eventual answer must not park a
	//: goroutine forever on a receiver that has moved on.
	answer := make(chan error, 1)
	go func() {
		//: the only statement this port ever sends.
		answer <- c.cfg.db.PingContext(probe)
	}()
	select {
	case err := <-answer:
		//: the database answered — with a failure or with nothing to say.
		return pingVerdict(err)
	case <-timer.C():
		//: announce, stop waiting, report. The ping goroutine finishes on
		//: its own and its answer is discarded by the buffered channel.
		cancel()
		//: a hang is a different operational fact from a refusal.
		return failed(HealthCheckTimeout, nil)
	}
}

// pingVerdict turns a ping result into the port's answer.
func pingVerdict(err error) error {
	//: the database is reachable.
	if err == nil {
		//: nothing to report.
		return nil
	}
	//: the driver's error travels beside the verdict; the verdict's Public
	//: never repeats it, because a dial failure names the host and the user.
	return failed(HealthCheckFailed, err)
}
