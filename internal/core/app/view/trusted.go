package view

import "html/template"

// TrustedHTML is an HTML fragment that the engine writes WITHOUT escaping.
//
// It is deliberately an ALIAS of html/template.HTML rather than a defined type
// of its own. That is not a convenience: html/template recognises its trust
// types by an exact type switch, so a `type TrustedHTML string` would be
// escaped like any other string — measured, it renders <b>hi</b> as
// &lt;b&gt;hi&lt;/b&gt; — and a trust type that does not actually confer trust
// is worse than none, because callers stop looking for the reason their markup
// disappeared.
//
// The alias has a consequence that is stated rather than hidden: because
// TrustedHTML and html/template.HTML are the same type, the engine cannot tell
// a fragment marked through [TrustHTML] from one a caller converted directly.
// What the SDK buys is therefore precise, and it is worth naming exactly:
//
//   - Six of html/template's seven trust types — CSS, HTMLAttr, JS, JSStr,
//     URL and Srcset — are REFUSED outright when they appear in render data,
//     with the path that carries them named in the error. Those six have no
//     SDK spelling at all.
//   - The seventh has one canonical, greppable spelling, [TrustHTML], so
//     "where does this codebase decide to trust markup?" is one grep with one
//     answer rather than a hunt through an import list.
//
// A build-time ban on the raw conversion in CONSUMER code belongs in
// tools/sdkguard (ADR 0033), which already reads consumer source for exactly
// this class of rule. It is not shipped here.
type TrustedHTML = template.HTML

// trustHTML is TrustHTML's body: decl_gen.go writes TrustHTML, from the
// design, as one call of it.
func trustHTML(s string) TrustedHTML {
	//: the conversion is the entire body — the spelling is the deliverable.
	return TrustedHTML(s)
}
