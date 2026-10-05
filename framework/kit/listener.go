package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// ListenHandler serves one connection of a [Listener] until it returns; the
// connection is closed after it. conn.Peer says who connected, as the kernel
// says where it can (pkg/v1/proc/ipc).
type ListenHandler = ikit.ListenHandler

// Listener is an inbound port that is not HTTP: a private socket on this
// machine — a Unix socket in a directory only the product's account can
// reach, the kernel naming the peer on Linux (ADR 0148) — speaking a
// versioned contract. It is how a daemon profile serves its clients, and how
// two process roles of one binary talk (D22): through the contract, never
// through each other's code.
type Listener = ikit.Listener

// ListenerOption tunes a listener.
type ListenerOption = ikit.ListenerConfigurer

// socketPath is SocketPath's body: decl_gen.go writes SocketPath, from the
// design, as one call of it.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func socketPath(path string) ListenerOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SocketPath(path)
}

// socketPer is SocketPer's body: decl_gen.go writes SocketPer, from the
// design, as one call of it.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func socketPer(scopes ...Scope) ListenerOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SocketPer(scopes...)
}

// allowPeers is AllowPeers's body: decl_gen.go writes AllowPeers, from the
// design, as one call of it.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func allowPeers(uids, gids []int) ListenerOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.AllowPeers(uids, gids)
}

// SocketPathFor is the socket of the listener name of service in product —
// the binary's name for a role of one, else the app's —, placed by default
// and named after scopes (SocketPer): the one rule, for a client of another
// process role that does not import the daemon's declarations (D22) — it
// names the three words and the scopes its contract states. The service is
// its qualified name for a module's.
func SocketPathFor(product, service, name string, scopes ...Scope) (string, error) {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.SocketPathFor(product, service, name, scopes...)
}
