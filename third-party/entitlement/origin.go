// Package entitlement - where the roster is fetched from. Authority comes from
// the vendor signature, never from the origin that served the bytes, so the
// binary is free to ask several places and keep whichever answers.
package entitlement

// OriginValue is one publication point for the roster pair.
//
// It carries no trust of its own: the vendor signature over the bytes is
// what authorises anything, so an origin is only ever an availability
// choice. That is precisely why the list can be extended freely — a hostile
// entry gains nothing, and a dead one is skipped.
type OriginValue struct {
	// Name identifies the origin in diagnostics. It is not part of the
	// trust decision — nothing here is — but an operator staring at a
	// refusal needs to know which endpoints were tried.
	Name string
	// BundleURL serves the signed bundle: roster and signature in one
	// document. ONE url, not two, because two objects cannot be fetched
	// atomically — see BundleValue for the five-minute cache window that made
	// a correct client refuse a correct roster.
	BundleURL string
}

// A product's Origins list every place its roster is published, in the order they are
// tried. Adding one costs nothing in safety: a hostile endpoint can serve
// whatever it likes and still cannot forge the vendor signature, which is
// the only thing that authorises anything.
//
// What it buys is availability. A single origin makes GitHub a hard
// dependency of starting the linter at all, and the roster's own 24h window
// means an outage that outlasts a daemon's grant blocks everyone. These
// three fail independently:
//
//   - The `licenses` branch is where the signing workflow publishes. It is
//     deliberately NOT the default branch: the default branch carries a
//     required-status ruleset, which refuses the bot's hourly push outright
//     ("Required status check post-commit is expected"). That is not a
//     hypothetical — it silently stopped every re-signature for a day and a
//     half, the roster's window closed, and every licensed binary refused to
//     start. A data branch under no such rule cannot be blocked that way.
//   - The Pages mirror is served by different infrastructure from
//     raw.githubusercontent.com, so it survives an outage of that one host.
//
// `main` is deliberately NOT listed. It served the roster until the state
// moved off it, and keeping a dead path in the list would cost a round trip
// on every fallback to reach a location nothing publishes to any more.
//
// Deliberately NOT included: jsDelivr. It mirrors the same repository and
// answers quickly, but caches a branch reference for hours — it would serve
// a roster whose window had already closed, turning an availability
// mechanism into the very refusal it exists to prevent.
