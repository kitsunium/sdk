//go:build linux

// Package loop — busy-poll, class A-code (#979, ADR 0189 amended 2026-11-11):
// the row on each native backend, the probe's refusal, the operator's switch
// that issues no call, and the parameters the kernel holds once opted in.
package loop

import (
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// busyPollCalls counts every REGISTER_NAPI and EPIOCSPARAMS a group issues,
// probe included.
func busyPollCalls(n *atomic.Int32) loopHooks {
	return loopHooks{busyPoll: func(string) { n.Add(1) }}
}

// TestBusyPoll_row pins the row on io_uring and epoll: class a-code, available
// without an opt-in, on with one, and missing with the stage named when the
// kernel refuses it — a kernel before 6.9, played by the probe hook.
func TestBusyPoll_row(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		plain, _ := serve(t, b, &lineH{}, nil)
		row := capRow(t, plain.State(), corenet.CapabilityBusyPoll)
		if row.Class != classCode {
			t.Fatalf("busy-poll class %q, want %q", row.Class, classCode)
		}
		if row.Status == statusAbsent {
			t.Skipf("busy-poll absent on %s: %s", kernelRelease(), row.Reason)
		}
		if row.Status != statusAvailable {
			t.Fatalf("busy-poll without an opt-in = %+v, want available", row)
		}
		var n atomic.Int32
		on, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.hooks = busyPollCalls(&n)
			c.Value.Loops = 2
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityBusyPoll}
		})
		if row := capRow(t, on.State(), corenet.CapabilityBusyPoll); row.Status != statusOn {
			t.Fatalf("busy-poll opted into = %+v, want on", row)
		}
		// one a loop, after the probe's: io_uring probes on each loop's thread,
		// epoll once for the group.
		want := map[corenet.LoopBackend]int32{corenet.LoopBackendIOURing: 4, corenet.LoopBackendEpoll: 3}[b.backend]
		if got := n.Load(); got != want {
			t.Errorf("busy-poll calls = %d, want %d: the probes' and one per loop", got, want)
		}
		for range 4 {
			echoBigClose(t, addr)
		}
		stage := map[corenet.LoopBackend]string{corenet.LoopBackendIOURing: "register_napi: ", corenet.LoopBackendEpoll: "epiocsparams: "}[b.backend]
		refused, _ := serve(t, b, &lineH{}, func(c *Config) {
			c.hooks = loopHooks{failStage: "busy_poll"}
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityBusyPoll}
		})
		if row := capRow(t, refused.State(), corenet.CapabilityBusyPoll); row.Status != statusMissing || !strings.HasPrefix(row.Reason, stage) {
			t.Fatalf("busy-poll refused by the kernel = %+v, want missing, reason %q…", row, stage)
		}
	})
}

// TestBusyPoll_disabled pins the operator's switch over an opt-in: the row
// reads disabled and no REGISTER_NAPI nor EPIOCSPARAMS is issued, probe
// included.
func TestBusyPoll_disabled(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		var n atomic.Int32
		g, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.hooks = busyPollCalls(&n)
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityBusyPoll}
			c.Disabled = []corenet.Capability{corenet.CapabilityBusyPoll}
		})
		if row := capRow(t, g.State(), corenet.CapabilityBusyPoll); row.Status != statusDisabled {
			t.Fatalf("busy-poll switched off = %+v, want disabled", row)
		}
		echoBigClose(t, addr)
		if got := n.Load(); got != 0 {
			t.Fatalf("busy-poll calls under the switch = %d, want 0", got)
		}
	})
}

// TestBusyPoll_epollParams reads back, with EPIOCGPARAMS, what each epoll
// loop's instance holds once busy-poll is on: the window rounded up to
// microseconds, the budget, and prefer_busy_poll.
func TestBusyPoll_epollParams(t *testing.T) {
	b := backendCase{"epoll", corenet.LoopBackendEpoll}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("epoll not present: %v", err)
	}
	g, _ := serve(t, b, &lineH{}, func(c *Config) {
		c.Value.Capabilities = []corenet.Capability{corenet.CapabilityBusyPoll}
		c.Value.BusyPoll = corenet.DurationValue(20*time.Microsecond + time.Nanosecond)
		c.Value.BusyPollBudget = 16
	})
	if row := capRow(t, g.State(), corenet.CapabilityBusyPoll); row.Status != statusOn {
		t.Skipf("busy-poll %s on %s: %s", row.Status, kernelRelease(), row.Reason)
	}
	var p epollParams
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(g.ctl.(*epollRunner).loops[0].epfd), epiocGParams, uintptr(unsafe.Pointer(&p))); errno != 0 {
		t.Fatalf("EPIOCGPARAMS: %v", errno)
	}
	if p.usecs != 21 || p.budget != 16 || p.prefer != 1 {
		t.Fatalf("epoll params %+v, want 21 µs, budget 16, prefer 1", p)
	}
}

// TestBusyPoll_values holds the two fields to their ranges, and an opt-in
// to class A-code: CheckCapabilities takes busy-poll.
func TestBusyPoll_values(t *testing.T) {
	ok := corenet.LoopValue{Capabilities: []corenet.Capability{corenet.CapabilityBusyPoll}, BusyPoll: corenet.DurationValue(time.Second), BusyPollBudget: 65535}
	if err := CheckCapabilities("g", ok); err != nil {
		t.Fatalf("busy-poll at its bounds refused: %v", err)
	}
	for _, c := range []struct {
		field string
		v     corenet.LoopValue
	}{
		{"BusyPoll", corenet.LoopValue{BusyPoll: -1}},
		{"BusyPoll", corenet.LoopValue{BusyPoll: corenet.DurationValue(time.Second + time.Nanosecond)}},
		{"BusyPollBudget", corenet.LoopValue{BusyPollBudget: -1}},
		{"BusyPollBudget", corenet.LoopValue{BusyPollBudget: 65536}},
	} {
		err := CheckCapabilities("g", c.v)
		if !errs.HasCode(err, corenet.CodeLoopValueInvalid) || fieldOf(err, "field") != c.field {
			t.Errorf("%s out of range: %v %v, want LoopValueInvalid naming it", c.field, err, errs.FieldsOf(err))
		}
	}
}

// TestBusyPoll_epollABI freezes struct epoll_params and its two requests
// against include/uapi/linux/eventpoll.h (6.9): _IOW and _IOR of type 0x8A,
// numbers 1 and 2, on 8 bytes, as this architecture encodes them.
func TestBusyPoll_epollABI(t *testing.T) {
	var p epollParams
	if unsafe.Sizeof(p) != 8 || unsafe.Offsetof(p.budget) != 4 || unsafe.Offsetof(p.prefer) != 6 || unsafe.Offsetof(p.pad) != 7 {
		t.Fatalf("struct epoll_params: size %d, budget %d, prefer %d, pad %d; want 8, 4, 6, 7",
			unsafe.Sizeof(p), unsafe.Offsetof(p.budget), unsafe.Offsetof(p.prefer), unsafe.Offsetof(p.pad))
	}
	want := func(dir uintptr, nr uintptr) uintptr { return dir<<iocDirShift | 8<<16 | 0x8A<<8 | nr }
	if epiocSParams != want(iocWrite, 1) || epiocGParams != want(iocRead, 2) {
		t.Fatalf("EPIOCSPARAMS %#x, EPIOCGPARAMS %#x; want %#x, %#x", epiocSParams, epiocGParams, want(iocWrite, 1), want(iocRead, 2))
	}
}

// TestBusyPoll_unprobed pins the portable backend's row: class a-code,
// unprobed.
func TestBusyPoll_unprobed(t *testing.T) {
	for _, row := range Unprobed("portable", nil, "") {
		if row.Name == corenet.CapabilityBusyPoll && (row.Class != classCode || row.Status != statusUnprobed) {
			t.Fatalf("portable busy-poll row %+v, want a-code unprobed", row)
		}
	}
}

// fieldOf is err's field key, "" when it has none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}
