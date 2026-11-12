package loop

import (
	"cmp"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The classes a capability row names (ADR 0189).
const (
	// classKernel is class A-kernel: on once the probe validates it.
	classKernel string = "a-kernel"
	// classCode is class A-code: on only when LoopValue.Capabilities opts in.
	classCode string = "a-code"
	// classB is class B: declared, never turned on by this step.
	classB string = "b"
)

// The statuses a capability row reports (ADR 0189).
const (
	// statusOn is probed and in use.
	statusOn string = "on"
	// statusAvailable is probed and present, not asked for.
	statusAvailable string = "available"
	// statusAbsent is probed, and the kernel lacks it.
	statusAbsent string = "absent"
	// statusMissing is a class A-code capability the code opted into and the
	// kernel lacks: server reports it as a fault.
	statusMissing string = "missing"
	// statusDisabled is switched off by the operator: never probed.
	statusDisabled string = "disabled"
	// statusUnprobed is of no use to the backend the group runs on.
	statusUnprobed string = "unprobed"
)

// reasonClassB is the reason every class B row gives.
const reasonClassB string = "class B: declared, not built until BENCH.md shows its threshold"

// maxAffinityCPU bounds LoopValue.Affinity: the CPU mask pinThread hands
// sched_setaffinity has that many bits.
const maxAffinityCPU int = 8191

// capClass is one capability the loop reports and its class, in the order
// State lists them.
type capClass struct {
	// name is the capability.
	name corenet.Capability
	// class is its class.
	class string
}

var (
	// optedElsewhere are the class A-code rows a loop group reports whose
	// opt-in is not LoopValue.Capabilities: reuseport-loops is Shards(0)'s
	// (#856), and reuseport-cbpf a datagram group's Steer (#977) — a loop
	// group attaches no steering program (#857).
	optedElsewhere = [...]corenet.Capability{corenet.CapabilityReuseportLoops, corenet.CapabilityReuseportCBPF}
	// loopCaps are the capabilities a loop group reports, class A-kernel first.
	// tcp-nodelay is server's socket probe, merged into the report by server.
	loopCaps = [...]capClass{
		{corenet.CapabilityURingMultishotRecv, classKernel},
		{corenet.CapabilityURingPbuf, classKernel},
		{corenet.CapabilityURingPbufInc, classKernel},
		{corenet.CapabilityURingDirect, classKernel},
		{corenet.CapabilityEpollExclusive, classKernel},
		{corenet.CapabilityTCPKeepAlive, classKernel},
		{corenet.CapabilityTCPUserTimeout, classKernel},
		{corenet.CapabilityURingDeferTaskrun, classCode},
		{corenet.CapabilityURingNoSQArray, classCode},
		{corenet.CapabilityURingRingFDs, classCode},
		{corenet.CapabilityURingSQRewind, classCode},
		{corenet.CapabilityURingRecvBundle, classCode},
		{corenet.CapabilityTCPDeferAccept, classCode},
		{corenet.CapabilityReuseportCBPF, classCode},
		{corenet.CapabilityReuseportLoops, classCode},
		{corenet.CapabilityLoopAffinity, classCode},
		{corenet.CapabilitySOIncomingCPU, classCode},
		{corenet.CapabilityTCPNotsentLowat, classCode},
		{corenet.CapabilityBusyPoll, classCode},
		{corenet.CapabilityURingSendZC, classCode},
		{corenet.CapabilityURingBundle, classB},
		{corenet.CapabilityURingZCRX, classB},
		{corenet.CapabilityURingMinTimeout, classB},
		{corenet.CapabilityTCPFastOpen, classB},
	}
)

// capClassOf is c's class, and false for a capability the loop does not report.
func capClassOf(c corenet.Capability) (string, bool) {
	//: the loop's own rows.
	for _, lc := range loopCaps {
		//: found.
		if lc.name == c {
			//: its class.
			return lc.class, true
		}
	}
	//: server's socket probe is class A-kernel too.
	if c == corenet.CapabilityTCPNoDelay {
		//: a-kernel.
		return classKernel, true
	}
	//: an engine, or no capability at all.
	return "", false
}

// CheckCapabilities refuses a LoopValue.Capabilities entry the group cannot opt
// into: an engine, a class B capability or an unknown token, with
// LoopCapabilityRefused naming it (ADR 0189). A class A-kernel entry needs no
// opt-in and is accepted as it is. server calls it for the portable backend,
// which never reaches Start.
func CheckCapabilities(group string, v corenet.LoopValue) error {
	//: the fields #857 added.
	if err := checkValue(group, v, v.Loops); err != nil {
		//: refused by name.
		return err
	}
	//: each entry.
	for _, c := range v.Capabilities {
		class, ok := capClassOf(c)
		//: anything but class A, or a row another option opts into.
		if !ok || class == classB || slices.Contains(optedElsewhere[:], c) {
			//: refused by name.
			return errs.Wrap(corenet.LoopCapabilityRefused, errs.WrapParams{},
				errs.String("group", group), errs.String("capability", string(c)))
		}
	}
	//: every entry is one the group may ask for.
	return nil
}

// checkValue refuses Affinity and NotsentLowat out of their ranges; loops is
// the group's loop count, a non-positive one not resolved yet, and then the
// count of Affinity is not judged.
func checkValue(group string, v corenet.LoopValue, loops int) error {
	field := ""
	//: by the first field out of range.
	switch {
	//: not one CPU per loop, once the count is known, or a CPU the mask has
	//: no bit for.
	case len(v.Affinity) > 0 && loops > 0 && len(v.Affinity) != loops,
		slices.ContainsFunc(v.Affinity, func(cpu int) bool { return cpu < 0 || cpu > maxAffinityCPU }):
		field = "Affinity"
	//: no negative byte count.
	case v.NotsentLowat < 0:
		field = "NotsentLowat"
	//: no negative budget (#809).
	case v.SlabBudget < 0:
		field = "SlabBudget"
	//: no negative floor (#969).
	case v.SendZCMin < 0:
		field = "SendZCMin"
	//: busy-poll's window and budget (#979), then ZCRX's fields (#980).
	default:
		field = cmp.Or(busyPollField(v), zcrxField(v))
	}
	//: in range.
	if field == "" {
		return nil
	}
	//: refused by name.
	return errs.Wrap(corenet.LoopValueInvalid, errs.WrapParams{},
		errs.String("group", group), errs.String("field", field), errs.Int("loops", loops))
}

// capSwitches is what the group may use: the operator's switches and the
// code's opt-ins, read once at Start.
type capSwitches struct {
	// off holds what the operator switched off, with offBy saying how.
	off   []corenet.Capability
	offBy string
	// want holds the class A-code capabilities the code opted into.
	want []corenet.Capability
}

// newSwitches reads cfg's switches.
func newSwitches(cfg *Config) capSwitches {
	by := cfg.DisabledBy
	//: the operator's variable unless told otherwise.
	if by == "" {
		by = corenet.EnvDisable
	}
	want := slices.Clone(cfg.Value.Capabilities)
	//: a field that asks for its row opts into it.
	if len(cfg.Value.Affinity) > 0 {
		want = append(want, corenet.CapabilityLoopAffinity)
	}
	//: a positive lowat asks for its row.
	if cfg.Value.NotsentLowat > 0 {
		want = append(want, corenet.CapabilityTCPNotsentLowat)
	}
	//: as configured.
	return capSwitches{off: cfg.Disabled, offBy: by, want: want}
}

// disabled reports whether the operator switched c off.
func (s capSwitches) disabled(c corenet.Capability) bool {
	//: listed.
	return slices.Contains(s.off, c)
}

// wants reports whether c may be used: class A-kernel unless switched off,
// class A-code when opted into and not switched off.
func (s capSwitches) wants(c corenet.Capability) bool {
	//: the operator wins.
	if s.disabled(c) {
		//: off.
		return false
	}
	class, _ := capClassOf(c)
	//: on by default, or opted into.
	return class == classKernel || slices.Contains(s.want, c)
}

// capReport is the rows a backend fills, one per loopCaps entry, in order,
// and the class A-code capabilities the code opted into.
type capReport struct {
	rows []corenet.CapabilityValue
	want []corenet.Capability
}

// newReport starts a report: every row unprobed by backend, a switched-off
// one disabled, a class B one named as such.
func newReport(s capSwitches, backend string) capReport {
	rows := make([]corenet.CapabilityValue, 0, len(loopCaps))
	//: one row each.
	for _, lc := range loopCaps {
		row := corenet.CapabilityValue{Name: lc.name, Class: lc.class, Status: statusUnprobed, Reason: "backend " + backend}
		//: by what applies first.
		switch {
		//: never probed.
		case s.disabled(lc.name):
			row.Status, row.Reason = statusDisabled, s.offBy
		//: never built.
		case lc.class == classB:
			row.Reason = reasonClassB
		}
		rows = append(rows, row)
	}
	//: ready to fill.
	return capReport{rows: rows, want: s.want}
}

// set records c's status and reason, unless the operator switched it off.
// An absent class A-code capability the code opted into is missing.
func (r *capReport) set(c corenet.Capability, status, reason string) {
	//: the row of c.
	for i := range r.rows {
		//: found, and not the operator's.
		if r.rows[i].Name == c && r.rows[i].Status != statusDisabled {
			//: asked for and absent: a fault.
			if status == statusAbsent && r.wanted(c) {
				status = statusMissing
			}
			r.rows[i].Status, r.rows[i].Reason = status, reason
		}
	}
}

// wanted reports whether the code opted into c, a class A-code capability.
func (r *capReport) wanted(c corenet.Capability) bool {
	//: listed.
	return slices.Contains(r.want, c)
}

// found records a probed capability: on when the group uses it, available
// when nobody asked for it, absent with reason when the kernel lacks it.
func (r *capReport) found(s capSwitches, c corenet.Capability, present bool, reason string) {
	//: by the probe and the switches.
	switch {
	//: the kernel lacks it.
	case !present:
		r.set(c, statusAbsent, reason)
	//: in use.
	case s.wants(c):
		r.set(c, statusOn, "")
	//: present, not asked for.
	default:
		r.set(c, statusAvailable, "")
	}
}

// Unprobed is the capability report of a group that runs on backend without
// the leaf's probes — the portable backend: every row unprobed, or disabled
// by what the operator switched off, as disabledBy says (ADR 0189).
func Unprobed(backend string, disabled []corenet.Capability, disabledBy string) []corenet.CapabilityValue {
	//: every row unprobed by backend, or disabled.
	return newReport(newSwitches(&Config{Disabled: disabled, DisabledBy: disabledBy}), backend).rows
}
