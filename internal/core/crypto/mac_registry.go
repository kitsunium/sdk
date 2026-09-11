// Package crypto — the process-wide MAC registry + MACTag / MACVerify dispatch.
package crypto

import (
	"fmt"

	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// macs maps each Algorithm to its MAC. Backed by the shared read-mostly
// schemeRegistry — register once at import, dispatch is lock-free.
var macs = schemeRegistry[MAC]{verb: "RegisterMAC"}

// RegisterMAC inserts m under m.Algorithm() and returns it so callers can bind
// the singleton to a typed package-level variable. Panics on a nil MAC or when
// a distinct MAC already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in MAC instances back to callers so each
// scheme keeps its concrete type unexported; the contract is the MAC interface.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterMAC(m MAC) MAC {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(m); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterMAC [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := macs.publish(m.Algorithm(), m); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the MAC lets callers bind it to a typed singleton var.
	return m
}

// LookupMAC returns the MAC registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in MAC instances behind the MAC
// interface — concrete types are intentionally unexported per scheme.
func LookupMAC(name Algorithm) (m MAC, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return macs.lookup(name)
}

// AvailableMACs returns the sorted list of registered MAC Algorithms.
func AvailableMACs() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return macs.available()
}

// MACTag returns the authentication tag over message under key for the MAC
// registered as name. It is named MACTag (not Tag) so the dispatch verb never
// collides with the signer/verifier surface. A name with no registered MAC
// returns UnknownMACAlgorithm (blank-import the scheme's package to register it).
func MACTag(name Algorithm, key Key, message []byte) (tag []byte, err error) {
	//: resolve the MAC; a missing import surfaces the typed sentinel.
	mac, ok := LookupMAC(name)
	//: absence path — the MAC package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownMACAlgorithm
	}
	//: delegate tagging; the redacting Key pins the length so Tag cannot fail.
	return mac.Tag(key, message), nil
}

// MACVerify reports whether tag authenticates message under key for the MAC
// registered as name, using the scheme's constant-time comparison. A name with
// no registered MAC returns (false, UnknownMACAlgorithm) so a miss is never
// mistaken for a bad-tag false.
func MACVerify(name Algorithm, key Key, message, tag []byte) (ok bool, err error) {
	//: resolve the MAC; a missing import is a configuration error, not a verdict.
	mac, found := LookupMAC(name)
	//: absence path — distinguish "scheme missing" from "bad tag".
	if !found {
		//: a miss returns false so callers never trust an unverified tag.
		return false, UnknownMACAlgorithm
	}
	//: a registered scheme reports validity as a plain bool, no error channel.
	return mac.Verify(key, message, tag), nil
}
