package loop

import corenet "github.com/kitsunium/sdk/internal/core/net"

// uring-zcrx's area and its probe's reasons (#980, ADR 0189 amended
// 2026-11-11).
const (
	// defaultZCRXArea is the area LoopValue.ZCRX takes when AreaBytes is
	// zero.
	defaultZCRXArea int = 4 << 20
	// reasonZCRXKernel is the row's reason on a kernel without the opcode.
	reasonZCRXKernel string = "kernel: IORING_OP_RECV_ZC unsupported (before 6.15); " + reasonClassB
	// reasonZCRXNone is the row's reason when the kernel has it and no
	// interface is named.
	reasonZCRXNone string = "available: no interface configured; " + reasonClassB
)

// zcrxField names the ZCRX field of v out of its range, "" when none is.
func zcrxField(v corenet.LoopValue) string {
	//: by the first out of range.
	switch {
	//: a queue index.
	case v.ZCRX.Queue < 0:
		//: named.
		return "ZCRX.Queue"
	//: a size.
	case v.ZCRX.AreaBytes < 0:
		//: named.
		return "ZCRX.AreaBytes"
	}
	//: in range.
	return ""
}

// zcrxOf resolves v.ZCRX: the area's default, rounded up to whole pages of
// page bytes.
func zcrxOf(v corenet.LoopValue, page int) corenet.ZCRXValue {
	z := v.ZCRX
	//: the default area.
	if z.AreaBytes == 0 {
		z.AreaBytes = defaultZCRXArea
	}
	z.AreaBytes = (z.AreaBytes + page - 1) / page * page
	//: resolved.
	return z
}
