// Package dbus is the SDK's message bus client (kitsunium/sdk#737, ADR 0214's
// S0e amendment): what a supervisor needs of a D-Bus daemon to run
// systemd's Type=dbus — who owns a well-known name, and every change of it —
// and what a supervised Go service needs to be one: owning its name. It is
// written on the standard library (ADR 0156): no vendor, no cgo, no
// reflection, no process launched.
//
// # The wire
//
// NewConn connects to the first unix socket the address names (unix:path=
// or unix:abstract=), sends the credentials byte and authenticates with AUTH
// EXTERNAL naming no identity — the kernel names the connecting user, a client
// in a user namespace included, and nothing is sent the bus could not check —,
// then says Hello and learns the connection's unique name.
// Every message is the specification's: a little-endian header, its fields
// as an array of (byte, variant), the body padded to 8; the client sends
// method calls whose arguments are strings and a uint32, and reads method
// returns, errors and signals of either byte order.
//
// # What a bus cannot do
//
// Hold a caller: every call waits for its reply 25 seconds at most —
// sd-bus's and libdbus's default reply timeout — or until its context's
// deadline when sooner (CALL_TIMEOUT); a cancelled context is
// CALL_CANCELLED. The authentication is bounded the same.
//
// # What a peer cannot do
//
// A header's field array is bounded (64 KiB) and a body past 1 MiB is read
// and dropped, never allocated: a peer that sends this connection a large
// message costs it nothing. A message the client cannot parse closes the
// connection (PROTOCOL_VIOLATION). A method call another peer sends this
// connection is dropped. NameOwnerChanged is taken from the bus driver
// alone — org.freedesktop.DBus, the sender the daemon itself stamps.
//
// # Watching a name
//
// WatchName asks the bus to announce the name's changes (AddMatch, its
// arg0 the name), then looks its owner up (GetNameOwner): the bus answers
// in order, so an announcement read before the lookup's reply is older than
// it and dropped, one read after it is newer and kept. The channel holds the
// lookup's owner, then every change in order; a watcher that falls behind
// by more than 64 changes has its latest ones coalesced into the last,
// never its oldest. It is closed when its context ends — the match removed
// — or when the connection is.
//
// # Platforms
//
// Linux. NewConn is UNSUPPORTED_PLATFORM elsewhere: the BSDs' daemons want
// the credentials byte carried with SCM_CREDS, macOS's bus is launchd's, and
// Windows' is a TCP address with a nonce file — none of which this client
// speaks.
package dbus
