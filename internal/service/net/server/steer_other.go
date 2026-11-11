//go:build !linux

package server

import (
	stdnet "net"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// attachSteer has no equivalent off Linux: SO_ATTACH_REUSEPORT_CBPF is
// Linux's, and the row then reads absent.
func attachSteer(sockets []stdnet.PacketConn) error {
	//: one socket or none: nothing to steer anywhere.
	if len(sockets) <= 1 {
		//: nothing to attach.
		return nil
	}
	//: the SDK's one answer for a mechanic the platform lacks.
	return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{}, errs.String("option", "steer"))
}
