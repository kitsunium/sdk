package dbus

import (
	"os"
	"strconv"
	"strings"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// A bus address (the D-Bus specification's "Server Addresses"): entries
// separated by ";", each "transport:key=value,…", a value's bytes outside
// [-0-9A-Za-z_/.\*] escaped as %XX. The client dials the first unix entry
// that names a socket path or an abstract name; it never launches a bus.

const (
	// systemBusDefault is the system bus's well-known socket.
	systemBusDefault string = "unix:path=/run/dbus/system_bus_socket"
	// sessionBusSocket is the session bus's socket under XDG_RUNTIME_DIR.
	sessionBusSocket string = "/bus"
	// hexBase is the base of a %XX escape.
	hexBase int = 16
	// escapeDigits is how many hex digits follow a % sign.
	escapeDigits int = 2
	// byteBits is the size of the byte an escape stands for.
	byteBits int = 8
	// hexDigits are the digits of an escape.
	hexDigits string = "0123456789abcdef"
)

// SystemBusAddress is the system bus's address: $DBUS_SYSTEM_BUS_ADDRESS, else
// unix:path=/run/dbus/system_bus_socket.
func SystemBusAddress() string {
	//: the environment's.
	if a := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); a != "" {
		//: as set.
		return a
	}
	//: the well-known socket.
	return systemBusDefault
}

// SessionBusAddress is the user's session bus's address:
// $DBUS_SESSION_BUS_ADDRESS, else unix:path=$XDG_RUNTIME_DIR/bus when
// XDG_RUNTIME_DIR is set, else empty — the client never launches a bus.
func SessionBusAddress() string {
	//: the environment's.
	if a := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); a != "" {
		//: as set.
		return a
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	//: no runtime directory: no bus to name.
	if dir == "" {
		//: none.
		return ""
	}
	//: the user bus's socket.
	return "unix:path=" + escapeValue(dir+sessionBusSocket)
}

// socketOf is the socket the first usable entry of address names: a path, or
// "@" and an abstract name, as package net dials it.
func socketOf(address string) (string, error) {
	//: each entry, in order.
	for entry := range strings.SplitSeq(address, ";") {
		//: a unix socket the client can dial.
		if socket, ok := unixSocket(entry); ok {
			//: the first.
			return socket, nil
		}
	}
	//: ADDRESS_INVALID.
	return "", errs.Wrap(coredbus.AddressInvalid, errs.WrapParams{}, errs.String("address", address))
}

// unixSocket reads one entry: unix: with exactly one of path= and abstract=,
// each value unescaped; any other transport or key is not one the client
// dials.
func unixSocket(entry string) (string, bool) {
	transport, params, found := strings.Cut(entry, ":")
	//: not a unix entry.
	if !found || transport != "unix" {
		//: skipped.
		return "", false
	}
	var socket string
	//: each key=value.
	for kv := range strings.SplitSeq(params, ",") {
		next, ok := unixKey(kv, socket)
		//: a key the client does not dial, or a malformed pair.
		if !ok {
			//: not dialled.
			return "", false
		}
		socket = next
	}
	//: a socket named.
	return socket, socket != ""
}

// unixKey reads one key=value of a unix entry over socket, the socket named
// so far: path= and abstract= name it, once; guid= is not checked; a
// listening key (tmpdir, dir, runtime), a second socket or a value that does
// not unescape is refused.
func unixKey(kv, socket string) (string, bool) {
	key, raw, ok := strings.Cut(kv, "=")
	value, vok := unescape(raw)
	//: a pair whose value unescapes.
	if !ok || !vok || value == "" {
		//: malformed.
		return "", false
	}
	//: by the key.
	switch {
	//: the server's identity: not checked.
	case key == "guid":
		//: unchanged.
		return socket, true
	//: a second socket.
	case socket != "":
		//: refused.
		return "", false
	//: a path.
	case key == "path":
		//: the socket.
		return value, true
	//: an abstract name.
	case key == "abstract":
		//: as package net names one.
		return "@" + value, true
	//: a listening key.
	default:
		//: refused.
		return "", false
	}
}

// unescape decodes a value's %XX escapes; a truncated or non-hex escape is
// refused.
func unescape(v string) (string, bool) {
	var b strings.Builder
	//: escape after escape.
	for {
		before, after, found := strings.Cut(v, "%")
		b.WriteString(before)
		//: no escape left.
		if !found {
			//: decoded.
			return b.String(), true
		}
		//: a truncated escape.
		if len(after) < escapeDigits {
			//: refused.
			return "", false
		}
		c, err := strconv.ParseUint(after[:escapeDigits], hexBase, byteBits)
		//: not hex.
		if err != nil {
			//: refused.
			return "", false
		}
		b.WriteByte(byte(c))
		v = after[escapeDigits:]
	}
}

// escapeValue escapes every byte of v outside [-0-9A-Za-z_/.\*].
func escapeValue(v string) string {
	var b strings.Builder
	//: each byte.
	for i := range len(v) {
		c := v[i]
		//: optionally escaped: kept.
		if busNameByte(c) || c == '/' || c == '.' || c == '\\' || c == '*' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0xf])
	}
	//: escaped.
	return b.String()
}
