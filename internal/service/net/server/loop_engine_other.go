//go:build !linux

package server

import (
	stdnet "net"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// startLoopEngine starts a loop group off Linux: the portable backend, the
// best this platform has in wave 1 (ADR 0186 §D5), so auto reaching it is no
// degradation. An explicit native backend is refused by the shared sentinel.
//
// IFACE-OPAQUE: the Linux build of this function chooses between the leaf's
// *loop.Group and the portable backend at Start; both builds return the same
// lifecycle interface so server never names the engine's type.
func startLoopEngine(group *StreamGroup, lns []stdnet.Listener) (loopEngine, error) {
	//: by the backend asked for.
	switch group.loop.Backend {
	//: no io_uring or epoll on this GOOS.
	case corenet.LoopBackendIOURing, corenet.LoopBackendEpoll:
		//: refused by name.
		return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", group.name), errs.String("backend", group.loop.Backend.String()))
	//: auto or portable.
	default:
		//: on the netpoller.
		return startPortable(group, lns, ""), nil
	}
}
