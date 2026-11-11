//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — uring-zcrx's floor, probe stage and ABI (#980, ADR 0189
// amended 2026-11-11). No kernel of the suite has IORING_OP_RECV_ZC: the
// stages past the opcode are played, and the register's own call is run
// only to see it refused and its ring and mappings released.
package loop

import (
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// zcrxCQE is struct io_uring_zcrx_cqe, a RECV_ZC completion's second half:
// the layout the receive path the ADR designs will read, frozen ahead of it.
type zcrxCQE struct {
	off uint64
	pad uint64
}

// cqe32 is a CQE of a ring set up with IORING_SETUP_CQE32: a RECV_ZC
// completion's struct io_uring_zcrx_cqe in its second half.
type cqe32 struct {
	cqe
	zc zcrxCQE
}

// TestURingABI_zcrx freezes zero-copy receive's layouts and numbers against
// include/uapi/linux/io_uring.h (6.15).
func TestURingABI_zcrx(t *testing.T) {
	var ifq zcrxIfqReg
	var area zcrxAreaReg
	var rd regionDesc
	var c cqe32
	for _, f := range []struct {
		name      string
		got, want uintptr
	}{
		{"sizeof(io_uring_zcrx_ifq_reg)", unsafe.Sizeof(ifq), 96},
		{"ifq_reg.area_ptr", unsafe.Offsetof(ifq.areaPtr), 16},
		{"ifq_reg.region_ptr", unsafe.Offsetof(ifq.regionPtr), 24},
		{"ifq_reg.offsets", unsafe.Offsetof(ifq.offsets), 32},
		{"ifq_reg.zcrx_id", unsafe.Offsetof(ifq.zcrxID), 64},
		{"sizeof(io_uring_zcrx_offsets)", unsafe.Sizeof(ifq.offsets), 32},
		{"sizeof(io_uring_zcrx_area_reg)", unsafe.Sizeof(area), 48},
		{"area_reg.flags", unsafe.Offsetof(area.flags), 24},
		{"area_reg.dmabuf_fd", unsafe.Offsetof(area.dmabufFD), 28},
		{"sizeof(io_uring_zcrx_rqe)", unsafe.Sizeof(zcrxRQE{}), 16},
		{"sizeof(io_uring_zcrx_cqe)", unsafe.Sizeof(zcrxCQE{}), 16},
		{"sizeof(io_uring_region_desc)", unsafe.Sizeof(rd), 64},
		{"region_desc.mmap_offset", unsafe.Offsetof(rd.mmapOffset), 24},
		{"sizeof(cqe32)", unsafe.Sizeof(c), 32},
		{"cqe32 zcrx_cqe", unsafe.Offsetof(c.zc), 16},
	} {
		if f.got != f.want {
			t.Errorf("%s = %d, want %d", f.name, f.got, f.want)
		}
	}
	for _, f := range []struct {
		name      string
		got, want uint64
	}{
		{"IORING_OP_RECV_ZC", uint64(opRecvZC), 58},
		{"IORING_REGISTER_ZCRX_IFQ", uint64(regZCRXIfq), 32},
		{"IORING_SETUP_CQE32", uint64(setupCQE32), 1 << 11},
		{"IORING_ZCRX_AREA_SHIFT", zcrxAreaShift, 48},
		{"IORING_MEM_REGION_TYPE_USER", uint64(memRegionTypeUser), 1},
	} {
		if f.got != f.want {
			t.Errorf("%s = %d, want %d", f.name, f.got, f.want)
		}
	}
	if uint32(opRecvZC) >= probeOps {
		t.Errorf("REGISTER_PROBE asks about %d ops: IORING_OP_RECV_ZC %d is past them", probeOps, opRecvZC)
	}
}

// zcrxRow serves an io_uring group with mod and returns its uring-zcrx row.
func zcrxRow(t *testing.T, mod func(*Config)) corenet.CapabilityValue {
	t.Helper()
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	g, addr := serve(t, b, &lineH{}, mod)
	echoBigClose(t, addr)
	row := capRow(t, g.State(), corenet.CapabilityURingZCRX)
	if row.Class != classB || row.Reason == reasonClassB {
		t.Fatalf("uring-zcrx row %+v: want class b and the stage that stops it, not the generic sentence", row)
	}
	return row
}

// TestZCRX_kernel reads the row the suite's kernel gives: absent before
// 6.15, available with no interface from it; never the generic sentence.
func TestZCRX_kernel(t *testing.T) {
	row := zcrxRow(t, nil)
	switch {
	case row.Status == statusAbsent && row.Reason == reasonZCRXKernel:
	case row.Status == statusAvailable && row.Reason == reasonZCRXNone:
	default:
		t.Fatalf("uring-zcrx on %s = %+v, want absent before 6.15, available after", kernelRelease(), row)
	}
	// a named queue on a kernel without the opcode is never registered.
	if row.Status == statusAbsent {
		named := zcrxRow(t, func(c *Config) { c.Value.ZCRX = corenet.ZCRXValue{Interface: "lo"} })
		if named.Reason != reasonZCRXKernel {
			t.Fatalf("a named queue before 6.15 = %+v, want the kernel's stage", named)
		}
	}
}

// TestZCRX_played plays each stage past the opcode on a kernel from 6.15:
// no interface, the register refused, the register accepted — the row
// class B and never on, whatever the stage.
func TestZCRX_played(t *testing.T) {
	lo := corenet.ZCRXValue{Interface: "lo", Queue: 3}
	for _, c := range []struct {
		name   string
		value  corenet.ZCRXValue
		errno  syscall.Errno
		status string
		reason string
	}{
		{"none", corenet.ZCRXValue{}, 0, statusAvailable, reasonZCRXNone},
		{"enodev", lo, syscall.ENODEV, statusAbsent, "register_zcrx_ifq lo queue 3: no such device; "},
		{"eperm", lo, syscall.EPERM, statusAbsent, "register_zcrx_ifq lo queue 3: operation not permitted; "},
		{"einval", lo, syscall.EINVAL, statusAbsent, "register_zcrx_ifq lo queue 3: invalid argument; "},
		{"accepted", lo, 0, statusAvailable, "register_zcrx_ifq lo queue 3: accepted on a throwaway ring; "},
	} {
		t.Run(c.name, func(t *testing.T) {
			row := zcrxRow(t, func(cfg *Config) {
				cfg.hooks = loopHooks{zcrxKernel: true, zcrxErrno: int(c.errno)}
				cfg.Value.ZCRX = c.value
			})
			if row.Status != c.status || !strings.HasPrefix(row.Reason, c.reason) {
				t.Fatalf("uring-zcrx = %+v, want %s, reason %q…", row, c.status, c.reason)
			}
		})
	}
}

// TestZCRX_switches pins the operator's switch, which wins over a played
// kernel and a named queue, and the opt-in, refused while the row is class
// B, with or without a queue named; a ZCRX field out of range is refused by
// name.
func TestZCRX_switches(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err == nil {
		g, _ := serve(t, b, &lineH{}, func(c *Config) {
			c.hooks = loopHooks{zcrxKernel: true}
			c.Value.ZCRX = corenet.ZCRXValue{Interface: "lo"}
			c.Disabled = []corenet.Capability{corenet.CapabilityURingZCRX}
		})
		if row := capRow(t, g.State(), corenet.CapabilityURingZCRX); row.Status != statusDisabled {
			t.Fatalf("uring-zcrx switched off = %+v, want disabled", row)
		}
	}
	for _, z := range []corenet.ZCRXValue{{}, {Interface: "lo"}} {
		err := CheckCapabilities("g", corenet.LoopValue{Capabilities: []corenet.Capability{corenet.CapabilityURingZCRX}, ZCRX: z})
		if !errs.HasCode(err, corenet.CodeLoopCapabilityRefused) {
			t.Errorf("uring-zcrx opted into with %+v: %v, want LoopCapabilityRefused", z, err)
		}
	}
	for field, z := range map[string]corenet.ZCRXValue{"ZCRX.Queue": {Queue: -1}, "ZCRX.AreaBytes": {AreaBytes: -1}} {
		err := CheckCapabilities("g", corenet.LoopValue{ZCRX: z})
		if !errs.HasCode(err, corenet.CodeLoopValueInvalid) || fieldOf(err, "field") != field {
			t.Errorf("%s out of range: %v, want LoopValueInvalid naming it", field, err)
		}
	}
	if z := zcrxOf(corenet.LoopValue{}, 4096); z.AreaBytes != defaultZCRXArea {
		t.Errorf("default area %d, want %d", z.AreaBytes, defaultZCRXArea)
	}
	if z := zcrxOf(corenet.LoopValue{ZCRX: corenet.ZCRXValue{AreaBytes: 1}}, 4096); z.AreaBytes != 4096 {
		t.Errorf("one byte rounds to %d, want a page", z.AreaBytes)
	}
}

// TestZCRX_registerRefused runs the register's own call on the suite's
// kernel and sees it refused — an unknown interface ENODEV before any ring;
// loopback EINVAL before 6.15 (no such register opcode), EPERM without
// CAP_NET_ADMIN, EINVAL or EOPNOTSUPP where loopback has no queue to split —
// on a locked thread, as a loop's.
func TestZCRX_registerRefused(t *testing.T) {
	if _, _, errno := openURing(probeEntries, 0); errno != 0 {
		t.Skipf("io_uring: %v", errno)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if errno := zcrxRegister(corenet.ZCRXValue{Interface: "kitsunium-none0", AreaBytes: 1 << 16}); errno != syscall.ENODEV {
		t.Fatalf("unknown interface: %v, want ENODEV", errno)
	}
	errno := zcrxRegister(corenet.ZCRXValue{Interface: "lo", AreaBytes: 1 << 16})
	switch errno {
	case syscall.EINVAL, syscall.EPERM, syscall.EOPNOTSUPP:
	default:
		t.Fatalf("loopback registered: %v, want refused", errno)
	}
	t.Logf("loopback on %s: %v", kernelRelease(), errno)
}

// TestZCRX_otherBackends pins the row off io_uring: unprobed on epoll and
// the portable backend.
func TestZCRX_otherBackends(t *testing.T) {
	for _, row := range Unprobed("portable", nil, "") {
		if row.Name == corenet.CapabilityURingZCRX && row.Status != statusUnprobed {
			t.Fatalf("portable uring-zcrx %+v, want unprobed", row)
		}
	}
	b := backendCase{"epoll", corenet.LoopBackendEpoll}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("epoll: %v", err)
	}
	g, _ := serve(t, b, &lineH{}, func(c *Config) { c.Value.ZCRX = corenet.ZCRXValue{Interface: "lo"} })
	if row := capRow(t, g.State(), corenet.CapabilityURingZCRX); row.Status != statusUnprobed {
		t.Fatalf("epoll uring-zcrx %+v, want unprobed", row)
	}
}
