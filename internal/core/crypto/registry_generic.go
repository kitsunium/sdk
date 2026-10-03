// Package crypto — the registrar every scheme registry shares (AEAD, Hasher,
// Signer, MAC, Deriver, Agreement, PasswordHasher, StreamSealer). The table
// under each one is the kernel's (kernel/plugin.Registry, ADR 0159); what this
// file adds is the crypto domain's refusal — the code, the reason and the
// fields a conflict panics with — written once instead of once per capability.
package crypto

import (
	"fmt"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// scheme is what every crypto registry stores: a comparable port that names its
// own Algorithm, the key it registers under. Every capability port satisfies it.
type scheme interface {
	comparable
	Algorithm() Algorithm
}

// schemeRegistry is one capability's registry: the kernel's read-mostly,
// copy-on-write table, and the public registrar's name, which is what its
// refusals carry. Re-registering the SAME value is an idempotent no-op; a
// DISTINCT value under a taken Algorithm is the hard conflict. The zero table
// is ready to use; a registry is declared with its verb.
type schemeRegistry[V scheme] struct {
	// table maps each Algorithm to its scheme; Lookup and Names read it
	// directly, lock-free.
	table plugin.Registry[Algorithm, V]
	// verb names the public registrar in panic/error text, e.g. "RegisterHasher".
	verb string
}

// register is the body of every public registrar: it refuses an unusable value,
// publishes v under v.Algorithm(), and returns v so a scheme package can bind
// its singleton to a typed package-level variable. Both refusals are boot-time
// panics carrying the duplicate-registration code.
//
// "Unusable" means a nil, a typed nil pointer or a value whose type is not
// comparable: each satisfies the port and none can serve one call (see
// internal/kernel/plugin). It is refused BEFORE v.Algorithm() is called, which
// is what lets a typed nil be refused at all.
func (r *schemeRegistry[V]) register(v V) V {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(v); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.%s [%s DUPLICATE_REGISTRATION]: %s", r.verb, CodeDuplicateRegistration, why))
	}
	//: a distinct scheme under a taken Algorithm is a hard conflict.
	if err := r.publish(v.Algorithm(), v); err != nil {
		//: the typed conflict's dotted-quad header, then what collided.
		panic(conflictText(err))
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return v
}

// publish inserts (name -> v). Returns a non-nil error when name is already
// held by a DISTINCT value; re-publishing the SAME value is an idempotent
// no-op. The duplicate check and the publish are one atomic step of the kernel
// table.
func (r *schemeRegistry[V]) publish(name Algorithm, v V) error {
	//: a free name or the same value under its own name: nothing to refuse.
	if !r.table.Publish(name, v) {
		//: published, or already there.
		return nil
	}
	//: a DISTINCT value under a taken name is the hard conflict: the typed
	//: sentinel, the fields naming the registrar and the name.
	return errs.Wrap(DuplicateRegistration, errs.WrapParams{},
		errs.String("registrar", "crypto."+r.verb), errs.String("algorithm", string(name)))
}
