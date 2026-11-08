package service

import (
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The settings of Type=dbus (S0e, kitsunium/sdk#737): BusName=, the
// BusWatcher that follows it, and the platform.

const (
	// maxBusName is the longest bus name the D-Bus specification allows.
	maxBusName int = 255
	// minBusElements is how many dot-separated elements a bus name has at
	// least.
	minBusElements int = 2
)

// defaultType is TypeDefault resolved as systemd's service_add_extras does:
// dbus with a BusName=, simple with an ExecStart=, oneshot without.
func defaultType(cfg *Config) coresvc.Type {
	//: by what the unit names.
	switch {
	//: a bus name: the unit is up on it.
	case cfg.BusName != "":
		//: dbus.
		return coresvc.TypeDbus
	//: a command to keep running.
	case len(cfg.ExecStart) > 0:
		//: simple.
		return coresvc.TypeSimple
	//: nothing to keep running.
	default:
		//: oneshot.
		return coresvc.TypeOneshot
	}
}

// resolveBus checks BusName= and, for Type=dbus, what follows it: Linux, a
// name, a BusWatcher. Every other type accepts a valid BusName= and does not
// watch it.
func resolveBus(cfg *Config, s *settings) error {
	//: a name the bus would refuse.
	if cfg.BusName != "" && !busNameValid(cfg.BusName) {
		//: CONFIG_INVALID naming it.
		return invalid("BusName", cfg.BusName)
	}
	//: no readiness on a bus name to resolve.
	if s.typ != coresvc.TypeDbus {
		//: the name, if any, is not watched.
		return nil
	}
	//: the SDK's watcher speaks Linux's credentials alone.
	if runtime.GOOS != osLinux {
		//: DIRECTIVE_UNSUPPORTED naming the type.
		return unsupported("Type", "dbus", runtime.GOOS)
	}
	//: systemd refuses a Type=dbus unit without a name.
	if cfg.BusName == "" {
		//: CONFIG_INVALID naming it.
		return invalid("BusName", "none for Type=dbus")
	}
	//: the name's owner could never be known.
	if cfg.BusWatcher == nil {
		//: CONFIG_INVALID naming the port.
		return invalid("BusWatcher", "none for Type=dbus")
	}
	s.busName = cfg.BusName
	//: watched from the first start on.
	return nil
}

// busNameValid reports a well-known bus name: two elements or more separated
// by dots, each of [A-Za-z0-9_-] and not opening with a digit, 255 bytes at
// most. A unique name (":1.42") is not one.
func busNameValid(name string) bool {
	//: too long, or empty.
	if len(name) > maxBusName || name == "" {
		//: invalid.
		return false
	}
	elements := strings.Split(name, ".")
	//: two elements or more, each one valid.
	return len(elements) >= minBusElements && !slices.ContainsFunc(elements, badBusElement)
}

// badBusElement reports an element of a bus name that is empty, opens with a
// digit, or holds a byte outside [A-Za-z0-9_-].
func badBusElement(e string) bool {
	//: empty, or opening with a digit.
	if e == "" || (e[0] >= '0' && e[0] <= '9') {
		//: bad.
		return true
	}
	//: a byte a name may not hold.
	return strings.IndexFunc(e, func(r rune) bool { return r >= utf8.RuneSelf || !busNameByte(byte(r)) }) >= 0
}

// busNameByte reports a byte of [A-Za-z0-9_-].
func busNameByte(c byte) bool {
	//: a letter, a digit, an underscore, a hyphen.
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-'
}
