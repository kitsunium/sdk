package transform

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// registry maps each Algorithm to its Compressor — the kernel's read-mostly,
// copy-on-write table (kernel/plugin.Registry, ADR 0159): schemes register
// exactly ONCE at import time and every other access is a lock-free Lookup.
// What stays here is the domain's refusal: the code and reason a conflict
// panics with.
var registry plugin.Registry[Algorithm, Compressor]

// Register inserts c into the registry under c.Algorithm() and returns it so
// callers can bind the singleton to a typed package-level variable like
// `var GzipCompressor = transform.Register(gzipCompressor{})`. Panics on a nil
// scheme or when a distinct scheme already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Compressor instances back to callers
// so each scheme keeps its concrete type unexported; the stable contract is the
// Compressor interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(c Compressor) Compressor {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(c); why != "" {
		//: panic with the DuplicateRegistration sentinel so the bracket header
		//: "[<0.2.5.5> DUPLICATE_REGISTRATION]" is a valid (code,reason) pairing —
		//: the sentinel's Error() already renders the dotted-quad code + reason.
		panic(DuplicateRegistration.Error() + ": " + why)
	}
	//: publish under the scheme name; a conflict turns into a boot-time panic.
	if err := publishCompressor(c.Algorithm(), c); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return c
}

// publishCompressor inserts (name -> c) into the registry. Returns a non-nil
// error when name is already registered to a different scheme; re-registering
// the same scheme is an idempotent no-op. The check and the insert are one
// atomic step of the kernel table.
func publishCompressor(name Algorithm, c Compressor) error {
	//: a free name or the same scheme under its own name: nothing to refuse.
	if !registry.Publish(name, c) {
		//: published, or already there.
		return nil
	}
	//: a DISTINCT scheme under a taken name is the hard conflict; origin-wins
	//: keeps the DuplicateRegistration code+reason so the bracket header pairs
	//: 0.2.5.5 with DUPLICATE_REGISTRATION, while the algorithm name rides along
	//: as a structured field (the empty WrapParams.Code is dropped from the
	//: trail per the poison-pill rule).
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("algorithm", string(name)))
}

// Lookup returns the Compressor registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the
// Compressor interface — concrete types are intentionally unexported per scheme.
func Lookup(name Algorithm) (c Compressor, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return registry.Lookup(name)
}

// Available returns the sorted list of registered Algorithms, or nil before
// any Register.
func Available() []Algorithm {
	//: sorted ascending, the caller's own slice.
	return registry.Names()
}
