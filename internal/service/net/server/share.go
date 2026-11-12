package server

import (
	"context"
	"io"
	stdnet "net"
	"runtime"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// shareRefusal refuses ShareAddress where it cannot apply (#1330): a platform
// without SO_REUSEPORT, with UnsupportedPlatform — Windows holds its addresses
// with SO_EXCLUSIVEADDRUSE instead —, then an address of a Unix socket, with
// familyRefusal (LoopOptionUnsupported for a stream group,
// PacketOptionUnsupported for a datagram one). It returns nil when the group
// did not ask.
func shareRefusal(group *StreamGroup, familyRefusal *errs.Error) error {
	//: the platform's answer, decided by build tag.
	return shareRefusalFor(group, familyRefusal, reusePortSupported())
}

// packetReuseRefusal is a datagram group's refusal of what its sockets
// cannot share: an explicit shard count on a Unix datagram socket (#977), then
// ShareAddress (#1330).
func packetReuseRefusal(shim *StreamGroup) error {
	//: Shards first, as before ShareAddress existed.
	if err := unixgramShardsRefusal(shim); err != nil {
		//: refused by name.
		return err
	}
	//: then ShareAddress.
	return shareRefusal(shim, corenet.PacketOptionUnsupported)
}

// shareRefusalFor is shareRefusal with the platform's SO_REUSEPORT answer
// passed in, so the refusal only Windows, Solaris and illumos give is
// exercised by the suite on every OS.
func shareRefusalFor(group *StreamGroup, familyRefusal *errs.Error, reusePort bool) error {
	//: no ShareAddress given.
	if !group.kinds.has(corenet.OptionShareAddress) {
		//: nothing to refuse.
		return nil
	}
	//: no option to share a port with, whatever the family.
	if !reusePort {
		//: the SDK's one answer for a missing mechanic (ADR 0018).
		return errs.Wrap(coreproc.UnsupportedPlatform, errs.WrapParams{},
			errs.String("group", group.name), errs.String("option", corenet.OptionShareAddress.String()),
			errs.String("goos", runtime.GOOS))
	}
	//: SO_REUSEPORT is an IP-socket option; a second bind on a path fails.
	for _, addr := range group.addrs {
		//: a family with no port to share.
		if !shardable(addr.Network) {
			//: refused by name.
			return errs.Wrap(familyRefusal, errs.WrapParams{},
				errs.String("group", group.name), errs.String("option", corenet.OptionShareAddress.String()),
				errs.String("network", addr.Network))
		}
	}
	//: every address can be shared.
	return nil
}

// mustProbe reports whether an address about to be opened as count sockets is
// probed first (#1330): only when the sockets share it through SO_REUSEPORT
// without the group asking to share it with others, and only when it names a
// port — an address asked as port 0 draws one nobody holds.
func mustProbe(addr corenet.AddressValue, count int, share bool) bool {
	//: one socket binds without SO_REUSEPORT and is refused by the kernel
	//: itself; a shared address is not probed on purpose.
	if count <= 1 || share {
		//: no probe.
		return false
	}
	_, port, err := stdnet.SplitHostPort(addr.Addr)
	//: a fresh ephemeral port, or an address the bind will refuse itself.
	if err != nil || port == "0" {
		//: no probe.
		return false
	}
	//: a port another socket may hold.
	return true
}

// probeAddress binds addr once, plainly — no SO_REUSEPORT, the options the
// standard library sets for a single listener —, and closes the socket at once
// (#1330). An address another socket holds is refused as a single listener's
// bind would refuse it: the shards that follow cannot join a holder unless
// both asked to share. A stream address is listened on for the instant
// between the bind and the close, which the standard library does not
// separate; nobody else serves the address then, so a connection arriving in
// that instant is refused either way. stream picks the socket's kind.
func probeAddress(ctx context.Context, addr corenet.AddressValue, stream bool) error {
	var cfg stdnet.ListenConfig
	var sock io.Closer
	var err error
	//: the socket the shards will be of.
	if stream {
		sock, err = cfg.Listen(ctx, addr.Network, addr.Addr)
	} else {
		sock, err = cfg.ListenPacket(ctx, addr.Network, addr.Addr)
	}
	//: held, or refused for another reason the shards would meet too.
	if err != nil {
		//: the refusal names the address.
		return bindRefusal(addr, err)
	}
	//: the probe served its purpose.
	if cerr := sock.Close(); cerr != nil {
		//: a probe that will not close still holds the address.
		return errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
			errs.String("address", addr.String()), errs.String("cause", cerr.Error()))
	}
	//: free when probed.
	return nil
}

// bindRefusal is the error of a bind of addr the OS refused with cause:
// AddressInUse when another socket holds the address, ListenFailed otherwise,
// both naming it.
func bindRefusal(addr corenet.AddressValue, cause error) error {
	//: EADDRINUSE, or WSAEADDRINUSE on Windows.
	if bindConflict(cause) {
		//: an address taken, said as such (#1330).
		return errs.Wrap(corenet.AddressInUse, errs.WrapParams{},
			errs.String("address", addr.String()), errs.String("cause", cause.Error()))
	}
	//: permission, an interface that does not exist, a name that does not
	//: resolve.
	return errs.Wrap(corenet.ListenFailed, errs.WrapParams{},
		errs.String("address", addr.String()), errs.String("cause", cause.Error()))
}
