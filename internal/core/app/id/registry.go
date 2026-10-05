package id

import (
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// registry maps each Scheme to its Generator — the kernel's read-mostly,
// copy-on-write table (kernel/plugin.Registry, ADR 0159): schemes register
// once at import, every other access is a lock-free Lookup. What stays here is
// the domain's refusal: the reserved empty Scheme and the conflict's code.
var registry plugin.Registry[Scheme, Generator]

// Register inserts g under g.Scheme() and returns it so callers can bind the
// singleton to a typed package-level variable like
// `var UUIDv4 = id.Register(uuidv4Gen{})`. Panics on a nil generator or when a
// distinct generator already claims the same Scheme.
//
// IFACE-PLUGIN: the registry hands plug-in Generator instances back to callers
// so each scheme keeps its concrete type unexported; the contract is the
// Generator interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func Register(g Generator) Generator {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(g); why != "" {
		//: panic so the offender is visible at boot with the dotted-quad code.
		panic(DuplicateRegistration.Error() + ": " + why)
	}
	//: publish under the scheme name; a conflict turns into a boot-time panic.
	if err := publishGenerator(g.Scheme(), g); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the generator lets callers bind it to a typed singleton var.
	return g
}

// publishGenerator inserts (name -> g) into the registry. Returns a non-nil
// error when name is the reserved empty Scheme or is already registered to a
// different generator; re-registering the same generator is an idempotent
// no-op. The check and the insert are one atomic step of the kernel table.
func publishGenerator(name Scheme, g Generator) error {
	//: Scheme("") is the reserved invalid zero value (see Scheme.Known, which
	//: hard-codes false for it). Publishing under it would create an entry the
	//: registry resolves via Lookup/New but Known() reports as absent — two
	//: accessors disagreeing about the same key. Reject at the boundary.
	if name == "" {
		//: DuplicateRegistration is the registry's boot-time sentinel; its code
		//: doc already covers "a nil scheme". The field names the offender.
		return errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("scheme", "<empty>"))
	}
	//: a free name or the same generator under its own name: nothing to refuse.
	if !registry.Publish(name, g) {
		//: published, or already there.
		return nil
	}
	//: a DISTINCT generator under a taken name is the hard conflict; origin-wins
	//: keeps DUPLICATE_REGISTRATION; the scheme rides as a field.
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{}, errs.String("scheme", string(name)))
}

// Lookup returns the Generator registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind the
// Generator interface — concrete types are intentionally unexported per scheme.
func Lookup(name Scheme) (g Generator, ok bool) {
	//: a lock-free snapshot read; a miss hands back nil AND false.
	return registry.Lookup(name)
}

// available is Available's body: decl_gen.go writes Available, from the
// design, as one call of it.
func available() []Scheme {
	//: sorted ascending, the caller's own slice.
	return registry.Names()
}
