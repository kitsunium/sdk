// Package id — TypeID generator (type prefix + UUIDv7 in lowercase base32).
package id

import (
	coreid "github.com/kitsunium/sdk/internal/core/id"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// typeIDAlphabet is Crockford base32 in LOWER case. TypeID renders lower
	// case where ULID renders upper; the 32 symbols and the bit packing are the
	// same, which is why both go through crockford32Encode.
	typeIDAlphabet string = "0123456789abcdefghjkmnpqrstvwxyz"
	// typeIDSeparator joins the type prefix to the base32 suffix.
	typeIDSeparator string = "_"
	// typeIDMaxPrefix is the inclusive maximum prefix length. The bound exists
	// so a type name cannot dwarf the identifier it labels — an id whose prefix
	// is longer than its 26-character payload has stopped being an identifier.
	typeIDMaxPrefix int = 63
	// typeIDMinLen is the shortest input ParseTypeID can even look at: the
	// separator plus the fixed-width suffix. It guards the slice arithmetic
	// ONLY — the prefix it leaves empty is then judged by validateTypePrefix,
	// so "_01h2…" is refused as an invalid PREFIX rather than as a short
	// string. Raising this bound to include the prefix would make the length
	// check answer a question the prefix rule answers better.
	typeIDMinLen int = len(typeIDSeparator) + crockfordChars
)

// typeIDGen stamps a fixed type prefix on a UUIDv7 rendered in lowercase
// Crockford base32 — "user_01h2xcejqtf2nbrexx3vqjhp41".
//
// Deliberately NOT registered under a Scheme. A TypeID without a prefix is not
// a degraded TypeID, it is a UUIDv7 with extra steps, and there is no
// non-arbitrary prefix the SDK could pick on the caller's behalf — so
// registering a "typeid" singleton would mean either inventing the caller's
// domain vocabulary or minting "_01h2…". Both are the silent degradation ADR
// 0031 exists to forbid, so the scheme is reachable only through NewTypeID and
// core/id.New("typeid") correctly reports UNKNOWN_SCHEME.
type typeIDGen struct {
	prefix string
}

// NewTypeID returns a TypeID Generator stamping prefix on every identifier. It
// is NOT added to the global registry — bind it to your own variable.
//
// prefix is validated strictly and an invalid one is REFUSED at construction
// (ADR 0031): the prefix is the entire value of the scheme, so a generator
// built from a bad one would mint identifiers that fail to parse everywhere
// else, one per call, with nothing to point at.
func NewTypeID(prefix string) (g coreid.Generator, err error) {
	//: refuse up-front so a bad prefix costs one error, not one per identifier.
	if vErr := validateTypePrefix(prefix); vErr != nil {
		//: propagate the typed rejection naming the clause that failed.
		return nil, vErr
	}
	//: a validated prefix yields a plain value generator.
	return typeIDGen{prefix: prefix}, nil
}

// Scheme implements core/id.Generator. The key is reported for diagnostics and
// symmetry with the other schemes; it is not registered (see typeIDGen).
func (typeIDGen) Scheme() coreid.Scheme {
	//: the scheme key — resolvable by name only through NewTypeID.
	return "typeid"
}

// New mints a fresh UUIDv7 and renders it as prefix + "_" + 26 lowercase
// Crockford base32 characters.
func (g typeIDGen) New() (newID string, err error) {
	//: the zero value carries no prefix and there is no default to fall back
	//: on — that is precisely why this scheme is not registered. Refuse rather
	//: than mint "_01h2…" (ADR 0031).
	if g.prefix == "" {
		//: the same sentinel the constructor returns, for the same reason.
		return "", InvalidPrefix
	}
	//: 16 raw bytes back the 128-bit UUIDv7 payload.
	var b [uuidRawLen]byte
	//: the leading 6 bytes carry the Unix-millisecond timestamp (big-endian).
	putUint48BE(b[:], clock.System.Now().UnixMilli())
	//: the remaining bytes (after the timestamp) are secure random.
	if rerr := readRandom(b[tsBytes:]); rerr != nil {
		//: propagate the wrapped entropy failure.
		return "", rerr
	}
	//: stamp version 7 (time-ordered) + the RFC variant bits.
	setUUIDBits(b[:], uuidVersion7)
	//: assemble the canonical rendering.
	return g.prefix + typeIDSeparator + crockford32Encode(b[:], typeIDAlphabet), nil
}

// FormatTypeID renders prefix and the canonical dashed-hex UUID uuid as a
// TypeID. It is the migration entry point: an existing UUID column becomes a
// typed identifier without reissuing anything.
//
// The UUID version is deliberately NOT enforced. New only ever mints v7, but
// refusing to label a v4 that already exists in a caller's database would make
// this function useless for the one job it has.
func FormatTypeID(prefix, uuid string) (typeID string, err error) {
	//: the prefix rules are the same whether the payload is fresh or existing.
	if vErr := validateTypePrefix(prefix); vErr != nil {
		//: propagate the typed rejection naming the clause that failed.
		return "", vErr
	}
	//: decode the dashed form to the raw 128 bits.
	raw, parseErr := parseUUID(uuid)
	//: a malformed UUID is reported with the rule that rejected it.
	if parseErr != nil {
		//: propagate the typed rejection unchanged.
		return "", parseErr
	}
	//: assemble the canonical rendering.
	return prefix + typeIDSeparator + crockford32Encode(raw[:], typeIDAlphabet), nil
}

// ParseTypeID splits the canonical TypeID s into its type prefix and the
// dashed-hex UUID its suffix encodes. Together with FormatTypeID it round-trips
// in both directions.
func ParseTypeID(s string) (prefix, uuid string, err error) {
	//: the rendering has a fixed-width suffix and at least one prefix char.
	if len(s) < typeIDMinLen {
		//: name the rule that rejected it without echoing the input.
		return "", "", errs.Wrap(Malformed, errs.WrapParams{},
			errs.String("rule", "length"), errs.Int("length", len(s)))
	}
	//: the suffix is fixed-width, so the split point is counted from the END.
	//: Splitting on the FIRST or LAST underscore would both be wrong: the
	//: prefix may legitimately contain underscores ("user_account_01h2…"), and
	//: the suffix alphabet never does.
	cut := len(s) - crockfordChars - len(typeIDSeparator)
	//: the separator must sit exactly there or the shape is wrong.
	if s[cut:cut+len(typeIDSeparator)] != typeIDSeparator {
		//: name the rule and the offset that failed.
		return "", "", errs.Wrap(Malformed, errs.WrapParams{},
			errs.String("rule", "separator"), errs.Int("offset", cut))
	}
	//: everything before the cut is the type prefix.
	prefix = s[:cut]
	//: it must satisfy the same rules the constructor enforces.
	if vErr := validateTypePrefix(prefix); vErr != nil {
		//: propagate the typed rejection naming the clause that failed.
		return "", "", vErr
	}
	//: decode the fixed-width suffix back to the raw 128 bits.
	raw, decErr := crockford32Decode(s[cut+len(typeIDSeparator):], typeIDAlphabet)
	//: a malformed suffix is reported with the rule that rejected it.
	if decErr != nil {
		//: propagate the typed rejection unchanged.
		return "", "", decErr
	}
	//: hand back the prefix and the canonical dashed-hex payload.
	return prefix, formatUUID(raw[:]), nil
}

// validateTypePrefix enforces the TypeID prefix shape: 1..typeIDMaxPrefix
// characters of lowercase ASCII, with '_' allowed only BETWEEN two letters.
//
// Strictness is the point. A prefix is compared, logged and routed on; allowing
// case or punctuation variation would let "User", "user " and "user" name the
// same entity in three ways, which is the confusion the scheme exists to
// remove. The returned error names the clause that failed and never echoes the
// prefix, so it is safe to log wherever the other sentinels are.
func validateTypePrefix(prefix string) error {
	//: an empty prefix is refused even though the upstream spec permits it —
	//: an identifier that does not name its type is a UUIDv7, and this SDK
	//: already has a scheme for that.
	if prefix == "" {
		//: name the clause; there is nothing to echo.
		return errs.Wrap(InvalidPrefix, errs.WrapParams{}, errs.String("rule", "empty"))
	}
	//: a prefix longer than the payload it labels is not an identifier.
	if len(prefix) > typeIDMaxPrefix {
		//: the length is a bounded int, safe to echo; the prefix is not.
		return errs.Wrap(InvalidPrefix, errs.WrapParams{},
			errs.String("rule", "too_long"), errs.Int("length", len(prefix)))
	}
	//: vet every byte against its position-dependent rule.
	for i := range len(prefix) {
		//: the position-dependent charset rule lives in its own predicate.
		if legalPrefixByte(prefix[i], i, len(prefix)) {
			//: nothing more to check at this position.
			continue
		}
		//: uppercase, digits, punctuation, non-ASCII, or a misplaced underscore.
		return errs.Wrap(InvalidPrefix, errs.WrapParams{},
			errs.String("rule", "charset"), errs.Int("offset", i))
	}
	//: every clause passed.
	return nil
}

// legalPrefixByte reports whether c is legal at offset i of a prefix of length
// total: lowercase ASCII anywhere, and '_' anywhere but the first or the last
// byte. That is the specification's ^([a-z]([a-z_]{0,61}[a-z])?)?$ exactly, so
// consecutive interior underscores ("user__account") are legal and a check
// that also required letters on both sides would refuse spec-valid prefixes.
func legalPrefixByte(c byte, i, total int) bool {
	//: lowercase ASCII is legal at every position.
	if c >= 'a' && c <= 'z' {
		//: the common case.
		return true
	}
	//: an underscore is legal only in the interior — a leading or trailing one
	//: would make "user_" and "user" render as two names for one type.
	return c == typeIDSeparator[0] && i > 0 && i < total-1
}
