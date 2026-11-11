//go:build linux

package server

import (
	"errors"
	"syscall"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/net/loop"
)

// startPacketLane hands a duplicated, non-blocking descriptor of bound's
// socket to a new io_uring lane, which owns it from then on.
func startPacketLane(s *Server, bound *boundPacketConn, group *PacketGroup) (loop.PacketLane, error) {
	fd, err := dupPacketFD(bound)
	//: no descriptor to hand.
	if err != nil {
		//: refused.
		return nil, err
	}
	name := group.name
	lane, err := loop.StartPacket(&loop.PacketConfig{
		Group: name, FD: fd, Lane: bound.laneIndex, Ceiling: packetSize(group.limits), Handler: group.loopHandler,
		Counters: loop.PacketCountersValue{
			Total: &s.total, Oversized: &s.oversized, Truncated: &s.datagrams.truncated,
			ReplyFailures: &s.datagrams.replyFailures, GSOOversized: &s.datagrams.gsoOversized, KernelDrops: &s.datagrams.kernelDrops,
		},
		OnGSOOff: func() { s.recordGSOOff(name) },
	})
	//: refused: nothing of the lane is left.
	if err != nil {
		//: as refused.
		return nil, err
	}
	//: serving.
	return lane, nil
}

// dupPacketFD duplicates bound's socket close-on-exec and non-blocking.
func dupPacketFD(bound *boundPacketConn) (int, error) {
	sc := packetSocket(bound.pc)
	//: no descriptor behind it.
	if sc == nil {
		//: refused.
		return -1, dupRefused(syscall.EBADF)
	}
	raw, err := sc.SyscallConn()
	//: no raw access.
	if err != nil {
		//: refused.
		return -1, dupRefused(err)
	}
	fd := -1
	var derr error
	cerr := raw.Control(func(s uintptr) {
		r, _, e := syscall.Syscall(syscall.SYS_FCNTL, s, syscall.F_DUPFD_CLOEXEC, 0)
		//: refused.
		if e != 0 {
			derr = e
			return
		}
		fd = int(r)
		derr = syscall.SetNonblock(fd, true)
	})
	//: the first failure.
	if cerr != nil || derr != nil {
		//: the descriptor, if any, closed.
		if fd >= 0 {
			swallowErr(syscall.Close(fd))
		}
		//: refused.
		return -1, dupRefused(errors.Join(cerr, derr))
	}
	//: duplicated.
	return fd, nil
}

// dupRefused is the refusal of a socket that cannot be handed to a lane.
func dupRefused(cause error) error {
	//: named, the batched reader serving it.
	return errs.Wrap(corenet.LoopBackendUnavailable, errs.WrapParams{},
		errs.String("stage", "dup"), errs.String("reason", cause.Error()))
}
