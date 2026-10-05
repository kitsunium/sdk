// Package plugin answers one question a process-wide registry must ask before
// it publishes anything: is this value usable as an entry at all?
//
// A registry stores values behind an interface and hands them back to every
// caller for the life of the process. Two shapes satisfy such an interface at
// compile time and cannot serve:
//
//   - a TYPED nil — (*gzipCompressor)(nil) is not == nil, so the guard every
//     registrar writes lets it through, it is stored, and every lookup returns
//     it. The failure surfaces at the first dispatch, arbitrarily far from the
//     registration that caused it;
//   - a value whose dynamic type is NOT COMPARABLE — a struct holding a slice,
//     a map or a function. Registries compare entries to tell an idempotent
//     re-registration from a conflict, and == on such a value is a runtime
//     panic naming Go's comparison rather than the duplicate plug-in.
//
// Both are programming errors at import time, which is why the answer is a
// reason string rather than an error: the registrar panics with its OWN
// dotted-quad code and reason, and only borrows the sentence that says why.
//
// Package plugin — Registry: the read-mostly, name-keyed table a process-wide
// registry stores its plug-ins in, once Unusable has said they can be stored.
package plugin
