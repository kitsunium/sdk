package server

import (
	"slices"
	"strconv"
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// The ranks of the lattice iouring > epoll > portable > goroutine (ADR 0188
// §D1). A rank is compared, never shown: State spells the capability.
const (
	// rankGoroutine is the goroutine engine, the bottom.
	rankGoroutine int = iota + 1
	// rankPortable is the portable loop backend, the loop family's floor.
	rankPortable
	// rankEpoll is epoll.
	rankEpoll
	// rankIOURing is io_uring, the top.
	rankIOURing
)

// rankKernel is a kernel capability's rank: below every engine, never a
// ceiling, never compared with one.
const rankKernel int = 0

// The words State uses for the environment.
const (
	// sourceEnvironment is OverrideValue.Source when a variable is set.
	sourceEnvironment string = "environment"
	// faultEnvironment is FaultValue.Source for an unknown token.
	faultEnvironment string = "environment"
	// unknownDisables is CapabilityValue.Reason for every optional capability
	// an unknown token switched off (R5: no optional capability).
	unknownDisables string = "unknown token: no optional capability"
)

// capabilityEntry is one row of the registry: a token, its rank, and whether
// KITSUNIUM_NET_DISABLE may switch it off.
type capabilityEntry struct {
	// token is the capability, as the variables spell it.
	token corenet.Capability
	// rank is its place in the lattice.
	rank int
	// switchable is true for an engine an operator may switch off; a floor is not.
	switchable bool
	// kernel is true for an optional kernel capability (ADR 0189), which
	// KITSUNIUM_NET_DISABLE switches off and KITSUNIUM_NET_ENGINE never names.
	kernel bool
}

// capabilityRegistry is design/net.yaml's registry, top first.
// TestCapabilityRegistry_matchesTheDesign holds it to the design's two blocks.
var capabilityRegistry = [...]capabilityEntry{
	{token: corenet.CapabilityIOURing, rank: rankIOURing, switchable: true},
	{token: corenet.CapabilityEpoll, rank: rankEpoll, switchable: true},
	{token: corenet.CapabilityPortable, rank: rankPortable, switchable: false},
	{token: corenet.CapabilityGoroutine, rank: rankGoroutine, switchable: false},
	{token: corenet.CapabilityURingMultishotRecv, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingPbuf, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingDirect, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityEpollExclusive, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityTCPNoDelay, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingDeferTaskrun, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingNoSQArray, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingRingFDs, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingSQRewind, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingSendZC, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingBundle, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingPbufInc, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityBusyPoll, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingZCRX, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityReuseportCBPF, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityTCPDeferAccept, rank: rankKernel, switchable: true, kernel: true},
	{token: corenet.CapabilityURingMinTimeout, rank: rankKernel, switchable: true, kernel: true},
}

// operatorEnv is what KITSUNIUM_NET_ENGINE and KITSUNIUM_NET_DISABLE say, read
// once by New, before any group is declared and so before any probe.
type operatorEnv struct {
	// override is what State reports.
	override corenet.OverrideValue
	// faults are the tokens the registry does not know.
	faults []corenet.FaultValue
	// ceiling is the highest rank the environment allows.
	ceiling int
	// disabled holds the ranks KITSUNIUM_NET_DISABLE switched off.
	disabled [rankIOURing + 1]bool
	// kernelOff are the kernel capabilities it switched off; noOptional is
	// set by an unknown token, which switches every one off (R5).
	kernelOff  []corenet.Capability
	noOptional bool
}

// readOperator reads both variables through lookup and applies R5 (ADR 0187
// §D8): an empty value is unset, an unknown token caps the engine at portable,
// and nothing the environment says raises a group above its code's choice.
func readOperator(lookup lookupFunc) operatorEnv {
	env := operatorEnv{ceiling: rankIOURing}
	var raw []string
	engine, _ := lookup(corenet.EnvEngine)
	//: an empty value is "not set".
	if engine = strings.TrimSpace(engine); engine != "" {
		raw = append(raw, corenet.EnvEngine+"="+engine)
		env.applyEngine(engine)
	}
	disable, _ := lookup(corenet.EnvDisable)
	//: an empty value is "not set".
	if disable = strings.TrimSpace(disable); disable != "" {
		raw = append(raw, corenet.EnvDisable+"="+disable)
		env.applyDisable(disable)
	}
	//: nothing set: the zero override.
	if len(raw) == 0 {
		//: no override.
		return env
	}
	env.override.Source = sourceEnvironment
	env.override.Raw = strings.Join(raw, " ")
	env.override.Applied = env.applied()
	//: read once.
	return env
}

// applyEngine caps the lattice at the named engine; an unknown one caps it at
// portable and is reported.
func (e *operatorEnv) applyEngine(token string) {
	entry, ok := lookupCapability(token)
	//: a token outside the registry, or a kernel capability, which is no engine.
	if !ok || entry.kernel {
		e.unknown(corenet.EnvEngine, token)
		return
	}
	e.ceiling = min(e.ceiling, entry.rank)
}

// applyDisable switches off each listed engine; a token outside the registry,
// or a floor, which nothing switches off, caps the lattice at portable and is
// reported.
func (e *operatorEnv) applyDisable(list string) {
	//: one token per comma; empty items say nothing.
	for item := range strings.SplitSeq(list, ",") {
		token := strings.TrimSpace(item)
		//: an empty item.
		if token == "" {
			continue
		}
		entry, ok := lookupCapability(token)
		//: only an engine the registry lets an operator switch off.
		if !ok || !entry.switchable {
			e.unknown(corenet.EnvDisable, token)
			continue
		}
		//: a kernel capability, listed once.
		if entry.kernel {
			if !slices.Contains(e.kernelOff, entry.token) {
				e.kernelOff = append(e.kernelOff, entry.token)
				e.override.Disabled = append(e.override.Disabled, entry.token)
			}
			continue
		}
		//: listed once in State, whatever the repetition.
		if !e.disabled[entry.rank] {
			e.disabled[entry.rank] = true
			e.override.Disabled = append(e.override.Disabled, entry.token)
		}
	}
}

// unknown records a token the registry does not know and caps the lattice at
// portable, with no optional capability (R5).
func (e *operatorEnv) unknown(variable, token string) {
	e.ceiling = min(e.ceiling, rankPortable)
	e.noOptional = true
	e.faults = append(e.faults, corenet.FaultValue{
		Source: faultEnvironment,
		Reason: variable + ": unknown token " + strconv.Quote(token) + ": engines capped at portable",
	})
}

// applied is the highest engine the environment allows, Disabled skipped.
func (e *operatorEnv) applied() corenet.Capability {
	//: top first.
	for _, entry := range capabilityRegistry {
		//: the first engine the environment lets through.
		if !entry.kernel && e.allows(entry.rank) {
			//: the ceiling, as State spells it.
			return entry.token
		}
	}
	//: the floor is never switched off.
	return corenet.CapabilityGoroutine
}

// allows reports whether the environment lets a group run on the engine of
// rank r.
func (e *operatorEnv) allows(r int) bool {
	//: io_uring receives into provided buffers only: without them it is
	//: never set up.
	if r == rankIOURing && e.kernelDisabled(corenet.CapabilityURingPbuf) {
		//: ruled out.
		return false
	}
	//: under the ceiling and not switched off.
	return r <= e.ceiling && !e.disabled[r]
}

// kernelDisabled reports whether the environment switched kernel capability c
// off, by name or by an unknown token.
func (e *operatorEnv) kernelDisabled(c corenet.Capability) bool {
	//: named, or every one.
	return e.noOptional || slices.Contains(e.kernelOff, c)
}

// kernelSwitches is what a loop group is told is off, and how State says it:
// every kernel capability under an unknown token, the listed ones otherwise.
func (e *operatorEnv) kernelSwitches() ([]corenet.Capability, string) {
	//: an unknown token: none at all.
	if e.noOptional {
		var all []corenet.Capability
		//: every kernel row.
		for _, entry := range capabilityRegistry {
			if entry.kernel {
				all = append(all, entry.token)
			}
		}
		//: all of them.
		return all, unknownDisables
	}
	//: as listed.
	return e.kernelOff, corenet.EnvDisable
}

// lookupCapability finds a token in the registry, exactly as spelled.
func lookupCapability(token string) (capabilityEntry, bool) {
	//: every row.
	for _, entry := range capabilityRegistry {
		//: found.
		if string(entry.token) == token {
			//: the row.
			return entry, true
		}
	}
	//: unknown.
	return capabilityEntry{}, false
}

// loopPlan is the backend a loop group starts on once the environment is
// applied, and whether a native failure may still fall back to portable.
type loopPlan struct {
	// backend is what the leaf is asked for, or LoopBackendPortable.
	backend corenet.LoopBackend
	// fallback is true when the code asked for auto: a native failure then
	// falls back to portable, reported, rather than refusing Start.
	fallback bool
	// disabled are the kernel capabilities the environment switched off,
	// disabledBy how State says it (ADR 0189).
	disabled   []corenet.Capability
	disabledBy string
}

// plan is min(code, environment) for a loop group whose code asked for code,
// on a platform with the native backends when native is true (ADR 0187 §D8):
// the highest backend at or below the code's that the environment allows. A
// backend the environment rules out is never probed: epoll and portable never
// reach io_uring_setup. Off Linux every loop group is portable, whatever the
// environment says, an explicit native backend having been refused already.
func (e *operatorEnv) plan(code corenet.LoopBackend, native bool) loopPlan {
	p := e.backendPlan(code, native)
	p.disabled, p.disabledBy = e.kernelSwitches()
	//: the backend and what it may not use.
	return p
}

// backendPlan is plan's backend half.
func (e *operatorEnv) backendPlan(code corenet.LoopBackend, native bool) loopPlan {
	//: no native backend on this platform, or the code asked for portable.
	if !native || code == corenet.LoopBackendPortable {
		//: the portable backend.
		return loopPlan{backend: corenet.LoopBackendPortable}
	}
	iouring := code != corenet.LoopBackendEpoll && e.allows(rankIOURing)
	epoll := e.allows(rankEpoll)
	auto := code == corenet.LoopBackendAuto
	//: the four answers, top first.
	switch {
	//: auto, both allowed: the leaf's own io_uring-then-epoll.
	case auto && iouring && epoll:
		//: as the code asked.
		return loopPlan{backend: corenet.LoopBackendAuto, fallback: true}
	//: io_uring allowed.
	case iouring:
		//: io_uring alone; auto keeps its fallback to portable.
		return loopPlan{backend: corenet.LoopBackendIOURing, fallback: auto}
	//: epoll allowed: io_uring is never set up.
	case epoll:
		//: epoll alone.
		return loopPlan{backend: corenet.LoopBackendEpoll, fallback: auto}
	//: nothing native left.
	default:
		//: the floor of the loop family.
		return loopPlan{backend: corenet.LoopBackendPortable}
	}
}
