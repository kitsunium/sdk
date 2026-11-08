// Package dbus is the SDK's message bus client, on the standard library: who
// owns a well-known name on a D-Bus daemon and every change of it — what
// proc/service's Type=dbus is up on — and a name a Go service owns.
//
//	conn, err := dbus.NewConn(ctx, dbus.SystemBusAddress())
//	cfg.BusWatcher = conn // proc/service: Type=dbus, BusName=
//	defer conn.Close()
//
// A service that is itself a Type=dbus unit owns its name with
// RequestName once it is ready to answer on it.
//
// # Bounds
//
// Unix sockets only (unix:path=, unix:abstract=), AUTH EXTERNAL only, no
// launched bus. A header past 64 KiB closes the connection
// ([CodeProtocolViolation]); a body past 1 MiB is dropped unread. A call
// waits for its context ([CodeCallCancelled]); the bus's own refusal is
// [CodeCallFailed], its name in the error field. Every call, and the
// authentication, waits 25 seconds at most — sd-bus's default reply timeout
// — or its context's deadline when sooner ([CodeCallTimeout]). Linux only:
// NewConn is UNSUPPORTED_PLATFORM elsewhere.
package dbus
