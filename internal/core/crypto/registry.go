// Package crypto — holds the process-wide AEAD registry. Service- and
// third-party-level scheme packages register themselves via package-level var
// initialisers when imported (no init()), mirroring core/data/codec.
package crypto

import (
	"fmt"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// aeads maps each Algorithm to its AEAD (the shared schemeRegistry); idIndex
// maps the 1-byte wire id to the same AEAD so Open can dispatch from the box
// header alone. Both are the kernel's read-mostly, copy-on-write table
// (kernel/plugin.Registry, ADR 0159). The id index is the AEAD's own, kept
// BESIDE the Algorithm table: the scheme registry is keyed by Algorithm,
// whereas Open resolves by the byte id in the frame.
var (
	aeads   = schemeRegistry[AEAD]{verb: "Register"}
	idIndex plugin.Registry[byte, AEAD]
)

// Register inserts a into the registry under a.Algorithm() + a.ID() and returns
// it so callers can bind the singleton to a typed package-level variable like
// `var AEAD = crypto.Register(aesGCM{})`. Panics on a nil scheme or when a
// distinct scheme already claims the same Algorithm or wire id.
//
// IFACE-PLUGIN: the registry hands plug-in AEAD instances back to callers so
// each scheme keeps its concrete type unexported; the stable contract is the
// AEAD interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(a AEAD) AEAD {
	//: refuse an unusable scheme, then publish it under its Algorithm first —
	//: both refusals panic at boot with the dotted-quad code.
	aeads.register(a)
	//: index the wire id second so Open can dispatch from the box header.
	if err := indexID(a.ID(), a); err != nil {
		//: the typed conflict's dotted-quad header, then what collided.
		panic(conflictText(err))
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return a
}

// indexID inserts (id -> a) into the wire-id index. Same idempotent-vs-conflict
// semantics as the scheme registry: the SAME scheme under its own id is a
// no-op, a DISTINCT one under a taken id is the conflict. The check and the
// insert are one atomic step of the kernel table.
func indexID(id byte, a AEAD) error {
	//: a free id or the same scheme under its own id: nothing to refuse.
	if !idIndex.Publish(id, a) {
		//: published, or already there.
		return nil
	}
	//: a DISTINCT scheme under a taken id is the hard conflict: the typed
	//: sentinel, the fields naming the registrar and the id.
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{},
		errs.String("registrar", "crypto.Register"), errs.String("wire_id", fmt.Sprintf("0x%02x", id)))
}

// Lookup returns the AEAD registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the AEAD
// interface — concrete types are intentionally unexported per scheme.
func Lookup(name Algorithm) (a AEAD, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return aeads.table.Lookup(name)
}

// lookupByID resolves the 1-byte wire id to its AEAD; used by Open to dispatch
// from the box header alone.
//
// IFACE-PLUGIN: returns the AEAD interface so the box id resolves to whichever
// scheme registered it; concrete scheme types stay unexported per package.
func lookupByID(id byte) (a AEAD, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return idIndex.Lookup(id)
}

// Available returns the sorted list of registered Algorithms, or nil before
// any Register.
func Available() []Algorithm {
	//: sorted ascending, the caller's own slice.
	return aeads.table.Names()
}
