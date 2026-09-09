// Package jwk — the JWK Set (RFC 7517 §5) and, with it, the package's second
// deliberate refusal.
//
// A set is how key rotation is published: for a window, the old and the new key
// are both in the document. RFC 7517 §4.5 only SHOULD-s distinct "kid" values,
// so a set holding two keys under one kid is legal and does happen. Resolving
// it by taking the first match would make the answer depend on JSON member
// order — an ordering no RFC guarantees and no publisher promises to keep.
//
// So ByKid refuses an ambiguous lookup (AmbiguousKid) and AllByKid is the
// rotation path: it returns every candidate, in document order, and the caller
// decides — typically by trying each until a signature verifies. Same principle
// as ADR 0031: where any SDK-chosen answer would be arbitrary, refuse instead
// of guessing quietly.
package jwk

import (
	"encoding/json"
	"slices"
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Set is an RFC 7517 §5 JWK Set: an ordered collection of keys published
// together. It is an immutable value — constructors copy in, accessors copy
// out — and it redacts under %v the same way KeyValue does.
type Set struct {
	// keys holds the members in document order, which AllByKid preserves so a
	// caller's "newest first" convention survives the round trip.
	keys []KeyValue
}

// NewSet builds a Set from keys, in the given order.
func NewSet(keys ...KeyValue) Set {
	//: clone so a later mutation of the caller's slice cannot reach the Set.
	return Set{keys: slices.Clone(keys)}
}

// ParseSet decodes a JWK Set document. Every member goes through Parse, so a
// set is accepted only when all of its keys are, and a member's own rejection
// code survives the wrapper (origin wins) instead of collapsing to "malformed".
//
// The "keys" member is required (RFC 7517 §5.1): an absent or null one is
// MissingMember, not an empty set. An empty ARRAY is a valid empty set — that
// is a publisher saying "no keys right now", which is different from a document
// that forgot to say anything.
func ParseSet(document []byte) (set Set, err error) {
	var raw setJSON
	//: the envelope first; members stay raw so Parse sees each one whole.
	if uerr := json.Unmarshal(document, &raw); uerr != nil {
		//: keep the decoder's cause — it names an offset, never the material.
		return Set{}, errs.Wrap(uerr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk.ParseSet: json.Unmarshal rejected the JWK Set envelope",
		})
	}
	//: nil covers both an absent and a null "keys"; an empty array does not.
	if raw.Keys == nil {
		//: a set document without its one required member.
		return Set{}, MissingMember
	}
	//: then every member, through the single validating entry point.
	return parseMembers(raw.Keys)
}

// parseMembers runs Parse over each raw member, failing the whole document on
// the first rejection.
func parseMembers(members []json.RawMessage) (set Set, err error) {
	keys := make([]KeyValue, 0, len(members))
	//: every member goes through the one validating entry point.
	for index, member := range members {
		key, perr := Parse(member)
		//: one bad member invalidates the set — a partially decoded JWKS is a
		//: rotation outage waiting to happen.
		if perr != nil {
			//: origin wins, so the member's code survives; the index locates it.
			return Set{}, errs.Wrap(perr, errs.WrapParams{}, errs.Int("index", index))
		}
		keys = append(keys, key)
	}
	//: every member validated.
	return Set{keys: keys}, nil
}

// Len reports how many keys the set holds.
func (s Set) Len() int {
	//: direct read; the zero Set is an empty set.
	return len(s.keys)
}

// Keys returns the set's members in document order.
func (s Set) Keys() []KeyValue {
	//: clone the slice; KeyValue itself exposes no mutable state.
	return slices.Clone(s.keys)
}

// AllByKid returns every key carrying kid, in document order — the rotation
// path. During a rollover a caller typically walks the candidates until one
// verifies, which is exactly the decision this package refuses to make for it.
//
// An empty kid returns no candidates rather than every keyless key: "match the
// keys that have no id" is never what a lookup by id means.
func (s Set) AllByKid(kid string) []KeyValue {
	//: an empty kid selects nothing, deliberately.
	if kid == "" {
		//: no candidates.
		return nil
	}
	var matches []KeyValue
	//: scan in document order so the caller's ranking survives.
	for _, key := range s.keys {
		//: exact match; a kid is an opaque string, never a pattern.
		if key.kid == kid {
			//: document order is preserved for the caller's own policy.
			matches = append(matches, key)
		}
	}
	//: zero, one or many — the caller's problem only in the many case.
	return matches
}

// ByKid returns THE key carrying kid.
//
// It refuses both ends of the ambiguity: no match is KeyNotFound, and more than
// one match is AmbiguousKid with the candidate count attached — never "the
// first one", which would silently depend on member order. Callers handling
// rotation use AllByKid and choose explicitly.
func (s Set) ByKid(kid string) (key KeyValue, err error) {
	//: empty kid resolves nothing (see AllByKid).
	if kid == "" {
		//: nothing to look up.
		return KeyValue{}, KeyNotFound
	}
	matches := s.AllByKid(kid)
	//: exactly one match is the only unambiguous answer.
	switch len(matches) {
	//: the set does not hold that key.
	case 0:
		//: no candidate carries that id.
		return KeyValue{}, KeyNotFound
	//: the single, unambiguous answer.
	case 1:
		//: exactly one candidate, so no choice to make.
		return matches[0], nil
	//: rotation window, or a publisher reusing an id — the caller decides.
	default:
		//: refuse, and say how many candidates there were.
		return KeyValue{}, errs.Wrap(AmbiguousKid, errs.WrapParams{},
			errs.Int("candidates", len(matches)))
	}
}

// MarshalPublic renders the set with every member in its public form — the
// document a JWKS endpoint serves.
//
// It marshals whole or not at all: one symmetric member makes the whole call
// fail with NoPublicForm (with the offending index attached) rather than
// silently dropping that key. A caller that publishes a set believing it holds
// five keys, and serves four, has a rotation outage nobody logged.
func (s Set) MarshalPublic() (document []byte, err error) {
	//: the public path is the one plain json.Marshal takes too.
	return s.marshalWith(KeyValue.MarshalPublic)
}

// MarshalPrivate renders the set with every member in its private form. Like
// KeyValue.MarshalPrivate, it is the only path that emits key material, and it
// is named so at the call site. A public-only member fails the whole call with
// NoPrivateMaterial.
func (s Set) MarshalPrivate() (document []byte, err error) {
	//: whole-or-nothing, same as the public path.
	return s.marshalWith(KeyValue.MarshalPrivate)
}

// MarshalJSON implements json.Marshaler by delegating to MarshalPublic, so a
// Set handed to plain json.Marshal — or embedded in an HTTP response struct —
// serialises its public form and nothing else.
func (s Set) MarshalJSON() (document []byte, err error) {
	//: the default must not be the dangerous one (ADR 0030).
	return s.MarshalPublic()
}

// marshalWith renders the set, encoding each member through render. Taking the
// per-key method as a parameter keeps the public and private envelopes
// identical — the two paths cannot drift into different set semantics.
func (s Set) marshalWith(render func(KeyValue) ([]byte, error)) (document []byte, err error) {
	//: non-nil even when empty, so an empty set emits "keys":[] and not null.
	raw := setJSON{Keys: make([]json.RawMessage, 0, len(s.keys))}
	//: render each member through the caller's chosen path.
	for index, key := range s.keys {
		member, merr := render(key)
		//: one unrenderable member fails the document.
		if merr != nil {
			//: origin wins, so the member's code survives; the index locates it.
			return nil, errs.Wrap(merr, errs.WrapParams{}, errs.Int("index", index))
		}
		raw.Keys = append(raw.Keys, member)
	}
	out, jerr := json.Marshal(raw)
	//: defensive — the members are already valid JSON.
	if jerr != nil {
		//: surface typed rather than dropping an impossible error.
		return nil, errs.Wrap(jerr, errs.WrapParams{
			Code:    CodeJWKMalformed,
			Reason:  "MALFORMED",
			Public:  "JWK document is malformed",
			Private: "service/crypto/jwk: json.Marshal failed on the JWK Set envelope",
		})
	}
	//: the rendered set.
	return out, nil
}

// String implements fmt.Stringer and reports only counts, so a stray %v on a
// set of private keys prints a shape, never material.
func (s Set) String() string {
	private := 0
	//: count the secret-bearing members without touching them.
	for _, key := range s.keys {
		//: count the members that would need MarshalPrivate to serialise.
		if key.IsPrivate() {
			//: one more secret-bearing member.
			private++
		}
	}
	//: counts only — never a member's rendering, let alone its material.
	return "jwk.Set{keys:" + strconv.Itoa(len(s.keys)) +
		" private:" + strconv.Itoa(private) + "}"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — without it fmt
// would walk the unexported members and print their octet slices.
func (s Set) GoString() string {
	//: same rendering; the point is that no path reaches the material.
	return s.String()
}
