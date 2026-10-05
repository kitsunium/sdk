package selfupdate

import "io"

// withAutomaticConsent is Service.WithAutomaticConsent's body: decl_gen.go writes Service.WithAutomaticConsent, from the
// design, as one call of it.
func (u *Service) withAutomaticConsent() *Service {
	//: a nil Service stays nil, as every With* here.
	if u == nil {
		//: nothing to configure.
		return nil
	}
	u.automatic = true
	return u
}

// withoutElevation is Service.WithoutElevation's body: decl_gen.go writes Service.WithoutElevation, from the
// design, as one call of it.
func (u *Service) withoutElevation() *Service {
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
