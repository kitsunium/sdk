//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package server

import (
	stdnet "net"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// nativePoller is none: no io_uring, epoll or kqueue here.
const nativePoller corenet.Capability = ""

// startLoopEngine starts a loop group off Linux, darwin and the BSDs: the
// portable backend, the best this platform has (ADR 0186 §D5, ADR 0222), so
// auto reaching it is no degradation, and the environment can lower nothing
// (ADR 0188). An explicit
// native backend is refused by the shared sentinel, whatever the environment.
//
// IFACE-OPAQUE: the Linux and BSD builds of this function choose between the leaf's
// *loop.Group and the portable backend at Start; both builds return the same
// lifecycle interface so server never names the engine's type.
func startLoopEngine(group *StreamGroup, lns []stdnet.Listener, plan loopPlan) (loopEngine, error) {
	//: by the backend asked for.
	switch group.loop.Backend {
	//: no io_uring, epoll or kqueue on this GOOS.
	case corenet.LoopBackendIOURing, corenet.LoopBackendEpoll, corenet.LoopBackendKqueue:
		//: refused by name.
		return nil, errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", group.name), errs.String("backend", group.loop.Backend.String()))
	//: auto or portable.
	default:
		//: on the netpoller.
		return startPortable(group, lns, "", plan), nil
	}
}
