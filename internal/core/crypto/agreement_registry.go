// Package crypto — the process-wide Agreement registry + GenerateAgreementKey / AgreementShared dispatch.
package crypto

import (
	"fmt"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/plugin"
)

// agreements maps each Algorithm to its Agreement scheme. Backed by the shared
// read-mostly schemeRegistry — register once at import, dispatch is lock-free.
var agreements = schemeRegistry[Agreement]{verb: "RegisterAgreement"}

// RegisterAgreement inserts a under a.Algorithm() and returns it so callers can
// bind the singleton to a typed package-level variable. Panics on a nil scheme
// or when a distinct scheme already claims the same Algorithm.
//
// IFACE-PLUGIN: the registry hands plug-in Agreement instances back to callers
// so each scheme keeps its concrete type unexported; the contract is the
// Agreement interface itself.
//
// "Nil" here means UNUSABLE, not only an untyped nil: a typed nil pointer and a
// plug-in whose type is not comparable both satisfy the port and neither can
// serve one call (see internal/kernel/plugin).
func RegisterAgreement(a Agreement) Agreement {
	//: a typed nil and a non-comparable plug-in both satisfy the port and
	//: neither can serve — refuse at import, where the offender is named.
	if why := plugin.Unusable(a); why != "" {
		//: panic so the offender is visible at boot.
		panic(fmt.Sprintf("crypto.RegisterAgreement [%s DUPLICATE_REGISTRATION]: %s", CodeDuplicateRegistration, why))
	}
	//: publish via the shared registry; a distinct duplicate Name is a hard conflict.
	if err := agreements.publish(a.Algorithm(), a); err != nil {
		//: surface the doc code for grep-friendly panic messages.
		panic(err.Error())
	}
	//: returning the scheme lets callers bind it to a typed singleton var.
	return a
}

// LookupAgreement returns the Agreement scheme registered under name.
//
// IFACE-PLUGIN: the registry stores plug-in Agreement instances behind the
// Agreement interface — concrete types are intentionally unexported per scheme.
func LookupAgreement(name Algorithm) (a Agreement, ok bool) {
	//: delegate to the shared registry's typed lookup.
	return agreements.lookup(name)
}

// AvailableAgreements returns the sorted list of registered agreement Algorithms.
func AvailableAgreements() []Algorithm {
	//: delegate to the shared registry's sorted key list.
	return agreements.available()
}

// GenerateAgreementKey draws a fresh keypair from the Agreement scheme
// registered as name. A name with no registered scheme returns
// UnknownAgreementAlgorithm; an entropy fault from the scheme propagates
// unchanged so callers see the original cause.
func GenerateAgreementKey(name Algorithm) (pub, priv []byte, err error) {
	//: resolve the scheme; a missing import surfaces the typed sentinel.
	agreement, ok := LookupAgreement(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, nil, UnknownAgreementAlgorithm
	}
	//: delegate keypair generation; the scheme owns its entropy source.
	return agreement.GenerateKey()
}

// AgreementShared derives the raw shared secret with the Agreement scheme
// registered as name. The result MUST be passed through a KDF before use as a
// key. A name with no registered scheme returns UnknownAgreementAlgorithm; a
// scheme Shared fault is wrapped into AgreementFailed, leaking no key bytes.
func AgreementShared(name Algorithm, priv, peerPub []byte) (secret []byte, err error) {
	//: resolve the scheme; a missing import surfaces the typed sentinel.
	agreement, ok := LookupAgreement(name)
	//: absence path — the scheme package was never blank-imported.
	if !ok {
		//: surface the documented sentinel naming the missing algorithm.
		return nil, UnknownAgreementAlgorithm
	}
	//: delegate the derivation to the scheme (it owns the curve arithmetic).
	out, derr := agreement.Shared(priv, peerPub)
	//: a scheme fault (e.g. a low-order peer point) relabels to AgreementFailed.
	if derr != nil {
		//: neutralise the cause so errs.Wrap's origin-wins cannot inherit a
		//: scheme *errs.Error's Code/Public/Private — the facade owns the
		//: boundary code and the "cause withheld" message regardless of the
		//: error type a third-party Shared returns (finding V21).
		return nil, errs.Wrap(neutralizeAgreementCause(derr), errs.WrapParams{
			Code:    CodeAgreementFailed,
			Reason:  "AGREEMENT_FAILED",
			Public:  "Key agreement failed to derive a shared secret",
			Private: "core/crypto.AgreementShared: the scheme rejected the inputs (cause withheld of key bytes)",
		})
	}
	//: hand back the raw secret for the caller to KDF.
	return out, nil
}

// neutralizeAgreementCause returns a cause safe to hand to errs.Wrap at the
// AgreementShared boundary. When the scheme returns a stdlib error it passes
// through unchanged (errs.Wrap takes the stdlib path, so AgreementFailed wins
// origin). When the scheme returns its own *errs.Error, origin-wins would
// otherwise inherit that error's Code/Public/Private — possibly leaking a
// diagnostic message or key bytes — so it is collapsed to an opaque
// agreementSchemeFault carrying no scheme Public/Private/Fields, forcing the
// facade's CodeAgreementFailed to stay the origin (finding V21).
func neutralizeAgreementCause(cause error) error {
	//: a scheme *errs.Error would win origin under errs.Wrap; collapse it.
	if _, isSDK := errs.CodeOf(cause); isSDK {
		//: opaque marker — never the scheme's Public/Private/Fields.
		return agreementSchemeFault{}
	}
	//: a plain stdlib cause is safe — errs.Wrap keeps AgreementFailed as origin.
	return cause
}

// agreementSchemeFault is the opaque, non-*errs.Error stand-in attached as the
// cause when an Agreement scheme returns its own typed error. It carries a
// fixed redacted message so no scheme diagnostic — and no key material — can
// ride out through the wrap chain, while still keeping a cause for chain walks.
type agreementSchemeFault struct{}

// Error reports a fixed redacted marker so the scheme's diagnostic never leaks.
func (agreementSchemeFault) Error() string {
	//: constant marker regardless of the scheme's original message.
	return "agreement scheme fault (cause withheld of key bytes)"
}
