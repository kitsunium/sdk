package dbus

import (
	"slices"
	"strings"
	"unicode/utf8"

	coredbus "github.com/kitsunium/sdk/internal/core/proc/dbus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// maxNameBytes is the longest bus name the specification allows.
	maxNameBytes int = 255
	// minElements is how many dot-separated elements a name has at least.
	minElements int = 2
)

// checkName refuses a name that is not a well-known bus name (NAME_INVALID).
func checkName(name string) error {
	//: well-known.
	if validName(name) {
		//: none.
		return nil
	}
	//: NAME_INVALID naming it.
	return errs.Wrap(coredbus.NameInvalid, errs.WrapParams{}, errs.String("name", name))
}

// validName reports a well-known bus name: two elements or more separated by
// dots, each of [A-Za-z0-9_-] and not opening with a digit, 255 bytes at
// most. A unique name (":1.42") is not one.
func validName(name string) bool {
	//: too long, or empty.
	if len(name) > maxNameBytes || name == "" {
		//: invalid.
		return false
	}
	elements := strings.Split(name, ".")
	//: two elements or more, each one valid.
	return len(elements) >= minElements && !slices.ContainsFunc(elements, badElement)
}

// badElement reports an element of a bus name that is empty, opens with a
// digit, or holds a byte outside [A-Za-z0-9_-].
func badElement(e string) bool {
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
