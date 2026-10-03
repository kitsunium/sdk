// Package trace — tracestate: the W3C vendor list that travels beside a
// traceparent.
package trace

import (
	"slices"
	"strings"
)

// The three limits the W3C tracestate grammar fixes. They are the GRAMMAR's own
// bounds, not ceilings this SDK invented, which is why there is no knob for any
// of them (ADR 0031 §refuse: an SDK-chosen ceiling here would be arbitrary, and
// the specification already supplies a non-arbitrary one).
const (
	// MaxTraceStateMembers is the list cap: `list = list-member 0*31( OWS ","
	// OWS list-member )` is 32 members, no more.
	MaxTraceStateMembers int = 32
	// MaxTraceStateKeyLen is the longest legal key: `simple-key = lcalpha
	// 0*255(...)` is 1 + 255 characters.
	MaxTraceStateKeyLen int = 256
	// MaxTraceStateValueLen is the longest legal value: `value = 0*255(chr)
	// nblk-chr` is 255 + 1 characters.
	MaxTraceStateValueLen int = 256
)

// The two sub-key limits of the multi-tenant form `tenant-id "@" system-id`:
// `tenant-id` is 1 + 240 characters and `system-id` is 1 + 13.
const (
	maxTenantIDLen int = 241
	maxSystemIDLen int = 14
)

// The list punctuation, named so the parser reads as the grammar does. They are
// strings rather than bytes because every use site splits or joins with them,
// and a byte would be converted at each one.
const (
	traceStateListSep string = ","
	traceStatePairSep string = "="
	traceStateTenant  string = "@"
	traceStateOWS     string = " \t"
)

// The bounds of `chr` — printable ASCII, %x20 through %x7E — plus the two
// punctuation marks it excludes, as bytes. The grammar states them as
// hexadecimal ranges, so they are named rather than inlined.
const (
	printableASCIILow  byte = 0x20
	printableASCIIHigh byte = 0x7E
	traceStateListByte byte = ','
	traceStatePairByte byte = '='
)

// StateValue is the parsed `tracestate` header: an ORDERED list of vendor
// entries, leftmost first.
//
// The order is load-bearing and is the reason this is not a map. §3.5 states it
// twice over: "the order of unmodified key/value pairs MUST be preserved" and
// "modified keys SHOULD be moved to the beginning (left) of the list", because
// the leftmost entry is the system that touched the trace most recently. A map
// would lose exactly that, and lose it silently.
//
// The value is IMMUTABLE: Insert and Delete return a new StateValue rather
// than mutating the receiver, so a span context handed to a handler cannot be
// edited underneath the caller that produced it. The zero value is a valid empty
// list.
//
// This SDK writes NO entry of its own. It is not a tracing vendor with state to
// carry, and inventing a key would put a name nobody registered on every
// outbound request; Insert exists for a consumer who IS one.
type StateValue struct {
	// entries is the ordered list, leftmost first. nil is the empty list.
	entries []traceStateEntry
}

// ParseTraceState reads a tracestate header value.
//
// It refuses the whole header rather than salvaging the members it understood.
// §4.3 permits exactly that — "if the tracestate header cannot be parsed the
// vendor MAY discard the entire header" — and salvaging is worse than it looks:
// a half-parsed list forwarded to the next hop is a list this process INVENTED,
// carrying somebody else's vendor key with entries silently missing.
//
// Empty and whitespace-only list members are SKIPPED rather than refused, which
// §3.3.1.1 requires: `list-member = (key "=" value) / OWS`.
//
// One deliberate leniency, and it is the only one: OWS is stripped around EVERY
// member, including before the first and after the last, where the `list` rule
// grants no OWS slot. A strictly-positioned parser would refuse "a=1 " — the
// value's last character must be nblk-chr — but RFC 7230 §3.2.4 already requires
// a recipient to strip leading and trailing whitespace from a field value before
// it is a field-value, so that check could only ever fire on input the HTTP layer
// is specified to have normalised, and it would pay for that by discarding
// another vendor's entire list over whitespace nobody can see. The nblk-chr rule
// is enforced in Insert instead, where it still prevents something.
func ParseTraceState(header string) (state StateValue, err error) {
	//: an absent or blank header is the empty list, not a failure.
	if strings.Trim(header, traceStateOWS) == "" {
		//: the zero value is a valid empty tracestate.
		return StateValue{}, nil
	}
	//: the list is comma-separated; OWS around each member is stripped below.
	fields := strings.Split(header, traceStateListSep)
	//: at most 32 real members can survive, so that is the capacity.
	entries := make([]traceStateEntry, 0, min(len(fields), MaxTraceStateMembers))
	//: one member at a time, in wire order.
	for _, field := range fields {
		//: parse it; present=false is the whitespace-only form §3.3.1.1 allows.
		entry, present, ok := parseTraceStateMember(field)
		//: a member the grammar cannot spell refuses the whole header.
		if !ok {
			//: refuse.
			return StateValue{}, InvalidTraceState
		}
		//: skip the whitespace-only members without spending a slot.
		if !present {
			//: nothing to record.
			continue
		}
		//: a repeated key makes the list ambiguous (§3.3.1.4), and the grammar
		//: caps the list at 32 — both refuse rather than salvage.
		if slices.ContainsFunc(entries, keyMatcher(entry.key)) || len(entries) == MaxTraceStateMembers {
			//: refuse.
			return StateValue{}, InvalidTraceState
		}
		//: the member is spellable; keep it in the order it arrived.
		entries = append(entries, entry)
	}
	//: a parsed, ordered, duplicate-free list.
	return StateValue{entries: entries}, nil
}

// parseTraceStateMember reads one `list-member`. It reports present=false for
// the whitespace-only form the grammar allows, and ok=false for anything the
// grammar cannot spell.
func parseTraceStateMember(field string) (entry traceStateEntry, present, ok bool) {
	//: strip the optional whitespace the grammar allows around a member.
	member := strings.Trim(field, traceStateOWS)
	//: `list-member = (key "=" value) / OWS` — the second alternative.
	if member == "" {
		//: legal, and it contributes nothing.
		return traceStateEntry{}, false, true
	}
	//: split on the FIRST '=' — the separator is excluded from `value`, so a
	//: later '=' would already have failed the value alphabet.
	key, value, found := strings.Cut(member, traceStatePairSep)
	//: a member with no '=' is neither a pair nor whitespace.
	if !found || !isValidTraceStateKey(key) || !isValidMemberValue(value) {
		//: unspellable.
		return traceStateEntry{}, false, false
	}
	//: a well-formed pair.
	return traceStateEntry{key: key, value: value}, true, true
}

// Len reports how many list members the state carries.
func (s StateValue) Len() int {
	//: nil entries is the empty list.
	return len(s.entries)
}

// Get returns the value recorded under key, and whether it was present.
func (s StateValue) Get(key string) (value string, ok bool) {
	//: linear over at most 32 entries, which beats a map at this size.
	at := slices.IndexFunc(s.entries, keyMatcher(key))
	//: absence path.
	if at < 0 {
		//: no entry under that key.
		return "", false
	}
	//: the recorded value.
	return s.entries[at].value, true
}

// Insert returns a copy of s carrying key=value at the FRONT of the list,
// replacing any existing entry for key.
//
// Front, not in place, because §3.5 says a modified key "SHOULD be moved to the
// beginning (left) of the list": leftmost means most recently touched, and that
// is the only information the order carries.
//
// When the result would exceed 32 members the OLDEST — rightmost — entry is
// dropped, which is §3.3.1.5's "the vendor MUST truncate whole entries". Its
// companion rule, "entries larger than 128 characters SHOULD be removed first",
// is deliberately NOT implemented: it is scoped to truncation "due to size
// limitations", and this list is bounded by member COUNT, never by byte length.
// Implementing it would drop a short-lived vendor's entry for being verbose
// rather than for being old, which is a different policy wearing the same name.
func (s StateValue) Insert(key, value string) (state StateValue, err error) {
	//: a key or value the grammar cannot spell would poison every downstream
	//: hop, so it is refused here rather than at format time.
	if !isValidTraceStateKey(key) || !isValidMemberValue(value) {
		//: nothing of the caller's input is echoed — it may be a secret.
		return StateValue{}, InvalidTraceState
	}
	//: room for the existing entries plus the new one.
	next := make([]traceStateEntry, 0, len(s.entries)+1)
	//: the updated entry leads the list.
	next = append(next, traceStateEntry{key: key, value: value})
	//: every other entry keeps its relative order behind it.
	for _, entry := range s.entries {
		//: the replaced key is not carried twice.
		if entry.key != key {
			//: preserved, in the order it had.
			next = append(next, entry)
		}
	}
	//: truncate whole entries from the right when the cap is exceeded.
	return StateValue{entries: next[:min(len(next), MaxTraceStateMembers)]}, nil
}

// Delete returns a copy of s without the entry under key. §3.5 asks a vendor
// not to delete keys it did not generate; this method is the mechanism, not the
// permission.
func (s StateValue) Delete(key string) StateValue {
	//: nothing to remove from an empty list.
	at := slices.IndexFunc(s.entries, keyMatcher(key))
	//: absence path — return the receiver unchanged, which is already a copy.
	if at < 0 {
		//: no entry under that key.
		return s
	}
	//: clone before deleting so the receiver's backing array is never touched.
	next := slices.Clone(s.entries)
	//: slices.Delete preserves the order of everything around the hole.
	return StateValue{entries: slices.Delete(next, at, at+1)}
}

// String renders the list as a tracestate header value: `key=value` members
// joined by a comma, leftmost first, with no optional whitespace.
//
// The OWS the grammar allows is not reproduced. It is optional, it carries no
// meaning, and omitting it makes the header a deterministic function of the
// list — which is what lets a test compare bytes rather than re-parse.
func (s StateValue) String() string {
	//: the empty list is the empty header, which a caller omits entirely.
	if len(s.entries) == 0 {
		//: nothing to render.
		return ""
	}
	//: one builder for the whole list; the grammar bounds its size.
	var out strings.Builder
	//: members in list order.
	for i, entry := range s.entries {
		//: every member but the first is preceded by the separator.
		if i > 0 {
			//: no OWS — see the method comment.
			out.WriteString(traceStateListSep)
		}
		//: key '=' value, exactly as the grammar spells a list member.
		out.WriteString(entry.key)
		out.WriteString(traceStatePairSep)
		out.WriteString(entry.value)
	}
	//: the rendered header value.
	return out.String()
}

// keyMatcher returns a predicate matching an entry by key. It is a closure over
// one string, built on the cold paths only.
func keyMatcher(key string) func(traceStateEntry) bool {
	//: key equality is the whole predicate.
	return func(entry traceStateEntry) bool { return entry.key == key }
}

// isValidTraceStateKey reports whether key matches `simple-key` or
// `multi-tenant-key`.
//
//	simple-key       = lcalpha 0*255( lcalpha / DIGIT / "_" / "-" / "*" / "/" )
//	multi-tenant-key = tenant-id "@" system-id
//	tenant-id        = ( lcalpha / DIGIT ) 0*240( ... )
//	system-id        = lcalpha 0*13( ... )
//
// The two forms differ in their first character — a tenant-id may start with a
// digit, a simple-key may not — which is why they are not one check with a
// longer length.
func isValidTraceStateKey(key string) bool {
	//: the multi-tenant form is the one carrying an '@'.
	tenant, system, multi := strings.Cut(key, traceStateTenant)
	//: a simple key is the common case.
	if !multi {
		//: 1..256 characters, first one a lowercase letter.
		return isTraceStateSegment(key, MaxTraceStateKeyLen, false)
	}
	//: a tenant-id may open with a digit; a system-id may not.
	return isTraceStateSegment(tenant, maxTenantIDLen, true) && isTraceStateSegment(system, maxSystemIDLen, false)
}

// isTraceStateSegment reports whether segment is 1..limit characters drawn from
// the key alphabet, opening with a lowercase letter — or, when digitLead, with a
// lowercase letter or a digit.
func isTraceStateSegment(segment string, limit int, digitLead bool) bool {
	//: length first: a key may not be empty and may not exceed its repetition
	//: count.
	if len(segment) == 0 || len(segment) > limit {
		//: outside the grammar.
		return false
	}
	//: the leading character is the only one with its own rule — a key may not
	//: open with '_', '-', '*' or '/', and only a tenant-id may open with a
	//: digit.
	lead := isLowerAlpha(segment[0]) || (digitLead && isDigit(segment[0]))
	//: every remaining character comes from the same six-way alphabet.
	return lead && allTraceStateKeyChars(segment[1:])
}

// allTraceStateKeyChars reports whether every byte of tail is drawn from the key
// alphabet `lcalpha / DIGIT / "_" / "-" / "*" / "/"`.
func allTraceStateKeyChars(tail string) bool {
	//: walk in place; the grammar bounds the length at 256.
	for i := range len(tail) {
		//: one character outside the alphabet disqualifies the segment.
		if !isTraceStateKeyChar(tail[i]) {
			//: outside the grammar.
			return false
		}
	}
	//: every character is spellable.
	return true
}

// isValidMemberValue reports whether value matches
// `value = 0*255(chr) nblk-chr`, i.e. 1..256 printable ASCII characters,
// excluding ',' and '=', whose LAST character is not a space.
//
// The trailing-space rule is the one that gets missed: it exists so that
// stripping OWS around a list member cannot change the value.
func isValidMemberValue(value string) bool {
	//: 1..256 characters, per the repetition count plus the mandatory nblk-chr —
	//: whose whole job is the last clause: a trailing space would be absorbed by
	//: OWS stripping at the next hop and silently change the value.
	if len(value) == 0 || len(value) > MaxTraceStateValueLen || value[len(value)-1] == ' ' {
		//: outside the grammar.
		return false
	}
	//: every character is printable ASCII other than ',' and '='.
	for i := range len(value) {
		//: one character outside `chr` disqualifies the value.
		if !isMemberValueChar(value[i]) {
			//: outside the grammar.
			return false
		}
	}
	//: every character is spellable.
	return true
}

// isTraceStateKeyChar reports whether c is drawn from the key alphabet
// `lcalpha / DIGIT / "_" / "-" / "*" / "/"`.
func isTraceStateKeyChar(c byte) bool {
	//: six alternatives, spelled in the grammar's own order.
	return isLowerAlpha(c) || isDigit(c) || c == '_' || c == '-' || c == '*' || c == '/'
}

// isMemberValueChar reports whether c is drawn from
// `chr = %x20 / %x21-2B / %x2D-3C / %x3E-7E` — printable ASCII minus ',' (0x2C)
// and '=' (0x3D).
func isMemberValueChar(c byte) bool {
	//: the two excluded punctuation marks are the list and pair separators.
	return c >= printableASCIILow && c <= printableASCIIHigh &&
		c != traceStateListByte && c != traceStatePairByte
}

// isLowerAlpha reports whether c is `lcalpha = %x61-7A`.
func isLowerAlpha(c byte) bool {
	//: a-z, and deliberately not A-Z: the grammar is lowercase-only.
	return c >= 'a' && c <= 'z'
}

// isDigit reports whether c is an ASCII digit.
func isDigit(c byte) bool {
	//: 0-9.
	return c >= '0' && c <= '9'
}
