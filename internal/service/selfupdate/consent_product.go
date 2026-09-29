// Package selfupdate — The consent a product declares, and the escalation it forbids (ADR 0150).
// Both are properties of the Service a product builds rather than fields of
// SourceValue: a source says where releases come from, a Service how this
// binary treats them.
package selfupdate

import "io"

// WithAutomaticConsent declares that the PRODUCT consents to unrequested
// upgrades — a status line that updates itself silently, its decision D11 —,
// so no environment variable has to grant it. An operator still refuses with
// <PREFIX>_AUTO_UPGRADE=0: an explicit answer always wins. It grants nothing
// else: escalation keeps its own, separate opt-in.
func (u *Service) WithAutomaticConsent() *Service {
	//: a nil Service stays nil, as every With* here.
	if u == nil {
		//: nothing to configure.
		return nil
	}
	u.automatic = true
	return u
}

// WithoutElevation forbids the privileged replacement outright, whatever
// <PREFIX>_ALLOW_SUDO says: a binary installed where its user cannot write is
// reported (ElevationNotAuthorised), never replaced through sudo.
func (u *Service) WithoutElevation() *Service {
	//: a nil Service stays nil, as every With* here.
	if u == nil {
		//: nothing to configure.
		return nil
	}
	u.elevate = u.src.refusedElevation
	return u
}

// AuthoriseUnattendedUpgrade decides whether an upgrade nobody asked for may
// proceed for this Service: an explicit environment answer first, then the
// product's own consent (WithAutomaticConsent), then the source's prompt rule.
func (u *Service) AuthoriseUnattendedUpgrade(out io.Writer, in io.Reader, interactive bool) bool {
	granted, decided := u.src.consentFromEnvironment()
	//: an operator's explicit answer overrules the product's standing one.
	if decided {
		//: the operator's answer.
		return granted
	}
	//: the product consented at build.
	if u.automatic {
		//: its standing consent.
		return true
	}
	//: otherwise the source's rule: ask a human, or refuse.
	return u.src.AuthoriseUnattendedUpgrade(out, in, interactive)
}
