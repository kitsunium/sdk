package memlimit

import (
	"math"
	"os"
	"runtime/debug"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxHeadroomPercent is the largest share of the allowance a caller may hand
// to the runtime: all of it.
const maxHeadroomPercent int = 100

// runtimeDefaultLimit is the soft memory limit the runtime runs with when
// GOMEMLIMIT is unset: none.
const runtimeDefaultLimit int64 = math.MaxInt64

// ConfigValue tunes a soft-memory-limit derivation. Its zero value is Apply's:
// 90 percent of the allowance, a 64 MiB floor, the GC percent untouched.
type ConfigValue struct {
	// HeadroomPercent is the share of the allowance handed to the runtime, in
	// (0, 100]; zero means 90. The rest covers what counts against the cgroup
	// outside the runtime — the binary, mapped files, C allocations — so a
	// cgo- or mmap-heavy service lowers it.
	HeadroomPercent int
	// MinimumLimit is the floor in bytes under which a derived limit is
	// declined (MemorySourceBelowFloor); zero means 64 MiB.
	MinimumLimit int64
	// GCPercent, when not nil, is installed with debug.SetGCPercent at the
	// moment a limit is — -1 runs the collector on the limit alone — and the
	// previous percent is put back when Watch withdraws the limit. Nil leaves
	// GOGC as it is.
	GCPercent *int
}

// resolvedConfig is a ConfigValue checked and with its zeros replaced by the
// defaults.
type resolvedConfig struct {
	// headroom is the share of the allowance handed to the runtime, in percent.
	headroom int64
	// floor is the smallest limit worth installing, in bytes.
	floor int64
	// gcPercent is the GC percent installed with a limit, or nil.
	gcPercent *int
}

// environment is what an installation touches: the environment, the cgroup
// files and the runtime's two knobs, injected so a test drives every branch.
type environment struct {
	// lookup reads an environment variable.
	lookup func(string) (string, bool)
	// readFile reads a cgroup or /proc file.
	readFile func(string) ([]byte, error)
	// setLimit installs a soft memory limit and returns the previous one.
	setLimit func(int64) int64
	// setGC installs a GC percent and returns the previous one.
	setGC func(int) int
}

// defaultConfig is Apply's configuration.
func defaultConfig() resolvedConfig {
	//: the package's constants.
	return resolvedConfig{headroom: headroomPercent, floor: minimumLimitBytes}
}

// systemEnvironment is the running process's.
func systemEnvironment() environment {
	//: the real environment, files and runtime.
	return environment{lookup: os.LookupEnv, readFile: os.ReadFile, setLimit: debug.SetMemoryLimit, setGC: debug.SetGCPercent}
}

// validate refuses a configuration no derivation can honour (ADR 0031).
func (c ConfigValue) validate() error {
	//: a share outside (0, 100], zero being the default.
	if c.HeadroomPercent < 0 || c.HeadroomPercent > maxHeadroomPercent {
		//: refused, with the value that was.
		return errs.Wrap(coreproc.MemlimitMisconfigured, errs.WrapParams{}, errs.Int("headroom_percent", c.HeadroomPercent))
	}
	//: a floor below zero, zero being the default.
	if c.MinimumLimit < 0 {
		//: refused, with the value that was.
		return errs.Wrap(coreproc.MemlimitMisconfigured, errs.WrapParams{}, errs.Int64("minimum_limit", c.MinimumLimit))
	}
	//: honourable.
	return nil
}

// resolved replaces the zeros with the defaults; the configuration is valid.
func (c ConfigValue) resolved() resolvedConfig {
	resolved := defaultConfig()
	resolved.gcPercent = c.GCPercent
	//: a share the caller chose.
	if c.HeadroomPercent != 0 {
		resolved.headroom = int64(c.HeadroomPercent)
	}
	//: a floor the caller chose.
	if c.MinimumLimit != 0 {
		resolved.floor = c.MinimumLimit
	}
	//: the configuration a derivation reads.
	return resolved
}

// ApplyWith is Apply with the caller's knobs: the share of the allowance handed
// to the runtime, the floor under which a derived limit is declined, and the
// GC percent installed with the limit. Its zero value is Apply.
//
// It returns MemlimitMisconfigured, and installs nothing, for a headroom
// outside (0, 100] percent or a negative floor (ADR 0031).
func ApplyWith(cfg ConfigValue) (coreproc.MemoryLimitValue, error) {
	//: the running process.
	return applyWithFrom(cfg, systemEnvironment())
}

// applyWithFrom is ApplyWith in env.
func applyWithFrom(cfg ConfigValue, env environment) (coreproc.MemoryLimitValue, error) {
	//: a configuration no derivation can honour installs nothing.
	if err := cfg.validate(); err != nil {
		//: refused.
		return coreproc.MemoryLimitValue{}, err
	}
	installer := governor{cfg: cfg.resolved(), env: env}
	derived := installer.derive()
	installer.settle(derived)
	//: what was read, derived and decided.
	return derived, nil
}

// governor installs derivations one after the other, and withdraws its own
// limit when a later derivation has none to install.
type governor struct {
	// cfg is the configuration every derivation reads.
	cfg resolvedConfig
	// env is what installing touches.
	env environment
	// gcSaved is the GC percent in force before gcPercent was installed.
	gcSaved int
	// installed reports that a limit of this governor's is in force.
	installed bool
	// gcSet reports that gcPercent is installed and gcSaved holds the previous.
	gcSet bool
}

// derive reads the files and derives, installing nothing.
func (g *governor) derive() coreproc.MemoryLimitValue {
	//: the configured derivation.
	return deriveWith(g.cfg, g.env.lookup, g.env.readFile)
}

// settle installs a derivation that carries a limit, and withdraws an earlier
// one when it does not.
func (g *governor) settle(derived coreproc.MemoryLimitValue) {
	//: a limit to install, and the GC percent with the first one.
	if derived.Applied() {
		g.env.setLimit(derived.Limit)
		g.installed = true
		//: once: the percent in force before is what a withdrawal puts back.
		if g.cfg.gcPercent != nil && !g.gcSet {
			g.gcSaved = g.env.setGC(*g.cfg.gcPercent)
			g.gcSet = true
		}
		return
	}
	//: nothing of ours in force: an operator's limit, or never a cap.
	if !g.installed {
		return
	}
	g.env.setLimit(runtimeDefaultLimit)
	g.installed = false
	//: GOGC=off with no limit would grow the heap without bound.
	if g.gcSet {
		g.env.setGC(g.gcSaved)
		g.gcSet = false
	}
}
