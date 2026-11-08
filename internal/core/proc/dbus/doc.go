// Package dbus is the contract of the SDK's message bus client
// (kitsunium/sdk#737, ADR 0214's S0e amendment): the codes its refusals
// carry, range 0.3.108.*, declared here at the engine's path (ADR 0160).
//
// The engine is internal/service/proc/dbus: a connection to a D-Bus daemon
// over a unix socket, written on the standard library — the authentication
// AUTH EXTERNAL, the messages the bus driver answers (Hello, AddMatch,
// GetNameOwner, RequestName, ReleaseName) and the NameOwnerChanged signal.
// It is what proc/service's Type=dbus watches a BusName= through; nothing
// here dials a socket.
//
// Package dbus — the sentinels the engine refuses with. Each var's name
// equals its Reason in SCREAMING_SNAKE form; none quotes a message's body in
// its Public or Private half.
package dbus
