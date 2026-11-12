//go:build linux

package loop

import (
	"syscall"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// soReusePort is SO_REUSEPORT (Linux 3.9, socket.h): absent from package
// syscall on every Linux architecture, cited here as ADR 0018 asks.
const soReusePort int = 0xF

// soIncomingCPU is SO_INCOMING_CPU (Linux 3.19, settable since 4.4,
// asm-generic/socket.h): absent from package syscall, cited here as ADR 0018
// asks.
const soIncomingCPU int = 49

// tcpNotsentLowat is TCP_NOTSENT_LOWAT (Linux 3.12, tcp.h): absent from
// package syscall, cited here as ADR 0018 asks.
const tcpNotsentLowat int = 25

// defaultNotsentLowat is the TCP_NOTSENT_LOWAT of an opt-in with no value:
// 128 KiB (#857).
const defaultNotsentLowat int = 128 << 10

// tcpUserTimeout is TCP_USER_TIMEOUT (Linux 2.6.37, tcp.h): absent from
// package syscall, cited here as ADR 0018 asks.
const tcpUserTimeout int = 0x12

// setKeepAlive sets SO_KEEPALIVE with the idle time and the interval of d
// (zero is keepAliveDefault) and keepAliveProbes probes, then reads the idle
// time back.
func setKeepAlive(fd int, d time.Duration) error {
	//: unset is the standard library's default.
	if d == 0 {
		d = keepAliveDefault
	}
	secs := max(ceilUnits(d, time.Second), 1)
	//: the option, then its three parameters.
	for _, o := range [...]struct{ level, opt, val int }{
		{syscall.SOL_SOCKET, syscall.SO_KEEPALIVE, 1},
		{syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE, secs},
		{syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL, secs},
		{syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT, keepAliveProbes},
	} {
		//: refused by the kernel.
		if err := syscall.SetsockoptInt(fd, o.level, o.opt, o.val); err != nil {
			//: the option and the kernel's answer.
			return err
		}
	}
	//: the kernel keeps what it was given.
	return expectOpt(fd, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE, secs)
}

// setUserTimeout sets TCP_USER_TIMEOUT to d in whole milliseconds and reads
// it back.
func setUserTimeout(fd int, d time.Duration) error {
	ms := ceilUnits(d, time.Millisecond)
	//: refused by the kernel.
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, tcpUserTimeout, ms); err != nil {
		//: the kernel's answer.
		return err
	}
	//: the kernel keeps what it was given.
	return expectOpt(fd, syscall.IPPROTO_TCP, tcpUserTimeout, ms)
}

// setDeferAccept sets TCP_DEFER_ACCEPT to d in whole seconds. The kernel
// rounds it to its retransmission schedule, so only a positive reading is
// required back.
func setDeferAccept(fd int, d time.Duration) error {
	//: refused by the kernel.
	if err := syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT, max(ceilUnits(d, time.Second), 1)); err != nil {
		//: the kernel's answer.
		return err
	}
	v, err := syscall.GetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_DEFER_ACCEPT)
	//: unreadable, or set to nothing.
	if err != nil || v <= 0 {
		//: not in effect.
		return errOr(err, syscall.EINVAL)
	}
	//: in effect.
	return nil
}

// optionRow is one capability row the listener options own and how to set it.
type optionRow struct {
	// name is the capability.
	name corenet.Capability
	// skip is why the group asks for nothing, empty when it does.
	skip string
	// apply sets the option on a TCP listener.
	apply func(fd int) error
	// what names the option for a refusal.
	what string
}

// optionRows are the rows the listener options own, as cfg asks for them.
func optionRows(cfg *Config, sw capSwitches) []optionRow {
	ka := optionRow{name: corenet.CapabilityTCPKeepAlive, what: "SO_KEEPALIVE", apply: func(fd int) error { return setKeepAlive(fd, cfg.KeepAlive) }}
	//: negative is the standard library's "off".
	if cfg.KeepAlive < 0 {
		ka.skip = "KeepAlive is negative"
	}
	ut := optionRow{name: corenet.CapabilityTCPUserTimeout, what: "TCP_USER_TIMEOUT", apply: func(fd int) error { return setUserTimeout(fd, cfg.UserTimeout) }}
	//: nothing asked for.
	if cfg.UserTimeout <= 0 {
		ut.skip = "no UserTimeout"
	}
	da := optionRow{name: corenet.CapabilityTCPDeferAccept, what: "TCP_DEFER_ACCEPT", apply: func(fd int) error { return setDeferAccept(fd, cfg.ReadTimeout) }}
	//: class A-code: opted into, and with a read bound to take.
	switch {
	//: never opted into.
	case !sw.wants(corenet.CapabilityTCPDeferAccept):
		da.skip = "not opted into"
	//: nothing to defer for.
	case cfg.ReadTimeout <= 0:
		da.skip = "no read bound"
	}
	lw := optionRow{name: corenet.CapabilityTCPNotsentLowat, what: "TCP_NOTSENT_LOWAT", apply: func(fd int) error { return setNotsentLowat(fd, lowatOf(cfg.Value)) }}
	//: class A-code: opted into, by Capabilities or by a positive value.
	if !sw.wants(corenet.CapabilityTCPNotsentLowat) {
		lw.skip = "not opted into"
	}
	//: in the order State lists them.
	return []optionRow{ka, ut, da, lw}
}

// lowatOf is the TCP_NOTSENT_LOWAT a group asks for: its value, or
// defaultNotsentLowat when only opted into.
func lowatOf(v corenet.LoopValue) int {
	//: the caller's own.
	if v.NotsentLowat > 0 {
		//: as given.
		return v.NotsentLowat
	}
	//: the default of an opt-in.
	return defaultNotsentLowat
}

// setNotsentLowat sets TCP_NOTSENT_LOWAT to n bytes and reads it back.
func setNotsentLowat(fd, n int) error {
	//: refused by the kernel (before 3.12).
	if err := setsockoptHook(fd, syscall.IPPROTO_TCP, tcpNotsentLowat, n); err != nil {
		//: the kernel's answer.
		return err
	}
	//: the kernel keeps what it was given.
	return expectOpt(fd, syscall.IPPROTO_TCP, tcpNotsentLowat, n)
}

// setsockoptHook is setsockopt for the rows #857 added, a test's refusing
// kernel when replaced.
var setsockoptHook = syscall.SetsockoptInt

// incomingCPURow sets SO_INCOMING_CPU on each per-loop listener whose loop
// Affinity pins, and reads it back: the kernel's SO_REUSEPORT lookup then
// prefers the listener whose CPU took the connection (#857). It needs both
// Shards(0)'s listeners and an Affinity; without either the row reads
// unprobed with what is missing.
func incomingCPURow(cfg *Config, sw capSwitches) listenerRow {
	row := listenerRow{name: corenet.CapabilitySOIncomingCPU, status: statusUnprobed, reason: "not opted into"}
	//: never opted into, or switched off (set keeps disabled).
	if !sw.wants(corenet.CapabilitySOIncomingCPU) {
		//: unprobed.
		return row
	}
	//: no CPU to name.
	if len(cfg.Value.Affinity) == 0 {
		row.reason = "no Affinity"
		//: unprobed.
		return row
	}
	row.reason = "no per-loop listener"
	//: each per-loop listener whose loop names a CPU.
	for _, l := range cfg.Listeners {
		//: shared, or a loop past Affinity.
		if l.Loop == 0 || l.Loop > len(cfg.Value.Affinity) {
			continue
		}
		//: refused: the fault, the group asked.
		if err := setIncomingCPU(l.FD, cfg.Value.Affinity[l.Loop-1]); err != nil {
			row.status, row.reason = statusMissing, "setsockopt SO_INCOMING_CPU: "+err.Error()
			//: the first refusal.
			return row
		}
		row.status, row.reason = statusOn, ""
	}
	//: on, or nothing to set it on.
	return row
}

// setIncomingCPU sets SO_INCOMING_CPU to cpu and reads it back.
func setIncomingCPU(fd, cpu int) error {
	//: refused by the kernel (before 4.4).
	if err := setsockoptHook(fd, syscall.SOL_SOCKET, soIncomingCPU, cpu); err != nil {
		//: the kernel's answer.
		return err
	}
	//: the kernel keeps what it was given.
	return expectOpt(fd, syscall.SOL_SOCKET, soIncomingCPU, cpu)
}

// tally is what the listener options found, one row each, over every TCP
// listener of the group.
type tally struct {
	// opts are the rows the options own, in State's order.
	opts []optionRow
	// rows are what each found so far.
	rows []listenerRow
	// anyTCP is whether the group has a TCP listener to ask anything of.
	anyTCP bool
}

// newTally starts a tally: every row unprobed, for want of a TCP listener.
func newTally(opts []optionRow) *tally {
	rows := make([]listenerRow, len(opts))
	//: until a listener says otherwise.
	for i, o := range opts {
		rows[i] = listenerRow{name: o.name, status: statusUnprobed, reason: "no TCP listener"}
	}
	//: nothing found yet.
	return &tally{opts: opts, rows: rows}
}

// listener sets every row the group asks for on the TCP listener fd and keeps
// what the kernel answered: the first refusal wins, and a later success never
// clears it.
func (t *tally) listener(fd int, sw capSwitches) {
	t.anyTCP = true
	//: each row.
	for i, o := range t.opts {
		//: switched off by the operator, or not asked for.
		if sw.disabled(o.name) || o.skip != "" {
			continue
		}
		err := o.apply(fd)
		//: by what the kernel answered.
		switch {
		//: refused.
		case err != nil:
			t.rows[i] = listenerRow{name: o.name, status: statusAbsent, reason: "setsockopt " + o.what + ": " + err.Error()}
		//: in effect, unless an earlier listener refused.
		case t.rows[i].status != statusAbsent:
			t.rows[i] = listenerRow{name: o.name, status: statusOn}
		}
	}
}

// done names what the group did not ask for, once every listener was seen: an
// unset option is present and unused, an opt-in never made or never given a
// bound is unprobed. A group with no TCP listener keeps "no TCP listener".
func (t *tally) done() []listenerRow {
	//: nothing to say of a group without TCP.
	if !t.anyTCP {
		//: as it was.
		return t.rows
	}
	//: each row not asked for.
	for i, o := range t.opts {
		//: asked for, or already answered.
		if o.skip == "" || t.rows[i].status != statusUnprobed {
			continue
		}
		t.rows[i].reason = o.skip
		//: an unset option is present and unused; an opt-in not made is not probed.
		if class, _ := capClassOf(o.name); class == classKernel {
			t.rows[i].status = statusAvailable
		}
	}
	//: the rows of State.
	return t.rows
}

// prepareListeners sets TCP_NODELAY and the listener options of ADR 0189's
// rows on every TCP listener of cfg, before any accept, so each accepted
// socket inherits them, and reads each back: what it found is the rows of
// State. A listener of another family, a Unix socket, takes none of them and
// is no refusal of the kernel's. It returns the descriptors in order.
func prepareListeners(cfg *Config, sw capSwitches) (fds []int, rows []listenerRow) {
	t := newTally(optionRows(cfg, sw))
	//: each listener of the group.
	for _, l := range cfg.Listeners {
		fds = append(fds, l.FD)
		//: only TCP has these options.
		if tcpListener(l.FD) {
			noDelay(l.FD, sw)
			t.listener(l.FD, sw)
		}
	}
	//: the group's descriptors and the rows.
	return fds, t.done()
}

// listenerRows is prepareListeners with the rows read off the per-loop
// listeners appended: reuseport-loops (#856) and so-incoming-cpu (#857).
func listenerRows(cfg *Config, sw capSwitches) (fds []int, rows []listenerRow) {
	fds, rows = prepareListeners(cfg, sw)
	//: the per-loop rows after the options'.
	return fds, append(rows, shardRow(cfg.Listeners), incomingCPURow(cfg, sw))
}
