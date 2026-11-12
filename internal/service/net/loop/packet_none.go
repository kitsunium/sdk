//go:build !linux || !(amd64 || arm64 || riscv64 || loong64)

package loop

import (
	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// StartPacket starts one datagram lane on io_uring (#852): a thread locked to
// it probes the kernel — the ring, RECVMSG and SENDMSG in REGISTER_PROBE, the
// provided-buffer ring, then a loopback multishot RECVMSG self-test — and only
// then serves cfg.FD. A failed stage is LoopBackendUnavailable naming it, the
// descriptor closed, and server serves the socket on its batched reader; off
// the io_uring architectures and off Linux it is LoopBackendUnavailable at the
// stage "arch", before anything is tried.
//
// Goroutine lifecycle: one per lane, locked to its OS thread, started here;
// it returns once Stop is asked or a handover drained the socket, and closes
// Done last.
//
// IFACE-OPAQUE: the lane's state is the loop thread's alone; server drives
// its life cycle and nothing else.
func StartPacket(cfg *PacketConfig) (PacketLane, error) {
	closePacketFD(cfg.FD)
	//: no io_uring here.
	return nil, packetUnavailable(cfg.Group)
}

// packetUnavailableReason is why a datagram lane never starts here.
const packetUnavailableReason string = "io_uring datagram lanes are Linux on amd64, arm64, riscv64 and loong64 only"

// packetUnavailable is the refusal of a platform with no io_uring lane.
func packetUnavailable(group string) error {
	//: the stage names the architecture, as the stream backend's refusal does.
	return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
		errs.String("group", group), errs.String("stage", "arch"), errs.String("reason", packetUnavailableReason))
}
