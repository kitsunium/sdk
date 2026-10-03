// Package trace — the tracestate header read into a list. The list and the
// grammar of its members are internal/core/observe/trace's StateValue; reading
// the header — its commas, its optional whitespace, its "=" — is this engine's
// mechanism (ADR 0160 §4).
package trace

import (
	"strings"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
)

// The header's own punctuation, named so the parser reads as the grammar does:
// `list = list-member 0*31( OWS "," OWS list-member )`, `list-member = (key "="
// value) / OWS`, `OWS = *( SP / HTAB )`. They are strings rather than bytes
// because every use site splits, cuts or trims with them.
const (
	traceStateListSep string = ","
	traceStatePairSep string = "="
	traceStateOWS     string = " \t"
)

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
// is enforced in coretrace.StateValue.Insert instead, where it still prevents
// something.
//
// Each member is handed to a coretrace.StateBuilder, which checks it against
// the member grammar, refuses a repeated key and a 33rd member — the list's
// invariants are the value's, and this function only reads the header.
func ParseTraceState(header string) (state coretrace.StateValue, err error) {
	//: an absent or blank header is the empty list, not a failure.
	if strings.Trim(header, traceStateOWS) == "" {
		//: the zero value is a valid empty tracestate.
		return coretrace.StateValue{}, nil
	}
	//: the list is comma-separated; OWS around each member is stripped below.
	fields := strings.Split(header, traceStateListSep)
	//: at most 32 real members can survive; the builder clamps the room.
	builder := coretrace.NewStateBuilder(len(fields))
	//: one member at a time, in wire order.
	for _, field := range fields {
		//: strip the optional whitespace the grammar allows around a member.
		member := strings.Trim(field, traceStateOWS)
		//: `list-member = (key "=" value) / OWS` — the second alternative is
		//: legal and contributes nothing, so it spends no slot.
		if member == "" {
			//: next member.
			continue
		}
		//: split on the FIRST '=' — the separator is excluded from `value`, so
		//: a later '=' fails the value alphabet in the builder.
		key, value, found := strings.Cut(member, traceStatePairSep)
		//: a member with no '=' is neither a pair nor whitespace; one the
		//: grammar cannot spell, a repeated key (§3.3.1.4) and a 33rd member
		//: are refused by the builder — each refuses the whole header.
		if !found || !builder.Add(key, value) {
			//: nothing of the header is echoed; a stranger wrote it.
			return coretrace.StateValue{}, coretrace.InvalidTraceState
		}
	}
	//: a parsed, ordered, duplicate-free list.
	return builder.State(), nil
}
