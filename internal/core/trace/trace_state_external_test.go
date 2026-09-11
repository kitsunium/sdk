package trace_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/trace"
)

// TestParseTraceStateAcceptsTheGrammar walks the shapes §3.3.1 permits.
func TestParseTraceStateAcceptsTheGrammar(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		members int
		rule    string
	}{
		{"the specification's example", "rojo=00f067aa0ba902b7,congo=t61rcWkgMzE", 2, "§3.3"},
		{"multi-tenant key", "fw529a3039@dt=foo", 1, "§3.3.1.3.1 multi-tenant-key"},
		{"optional whitespace", "a=1 ,  b=2", 2, "§3.3.1.2 OWS around a list member"},
		{"empty list members", "a=1,,,b=2", 2, "§3.3.1.1 empty and whitespace-only members are allowed"},
		{"whitespace-only member", "a=1,   ,b=2", 2, "§3.3.1.1 list-member = (key = value) / OWS"},
		{"value with spaces inside", "a=one two", 1, "§3.3.1.3.2 chr includes %x20"},
		{"key alphabet", "a-b_c*d/e=1", 1, "§3.3.1.3.1 lcalpha / DIGIT / _ - * /"},
		{"tenant may start with a digit", "9abc@dt=x", 1, "§3.3.1.3.1 tenant-id = ( lcalpha / DIGIT ) ..."},
		{"empty header", "", 0, "an absent list is the empty list"},
		{"whitespace-only header", "   ", 0, "an absent list is the empty list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := coretrace.ParseTraceState(tc.header)
			if err != nil {
				t.Fatalf("%s: ParseTraceState(%q): %v", tc.rule, tc.header, err)
			}
			if state.Len() != tc.members {
				t.Errorf("%s: Len = %d, want %d", tc.rule, state.Len(), tc.members)
			}
		})
	}
}

// TestParseTraceStateRefusals walks the refusals, each with its section.
//
// The whole header is refused rather than salvaged, which §4.3 permits ("the
// vendor MAY discard the entire header"). Salvaging is worse than it looks: a
// half-parsed list forwarded to the next hop is a list this process INVENTED,
// carrying somebody else's vendor key with entries silently missing.
func TestParseTraceStateRefusals(t *testing.T) {
	cases := []struct {
		name   string
		header string
		rule   string
	}{
		{"no equals", "novalue", "§3.3.1.2 list-member = key = value"},
		{"empty key", "=1", "§3.3.1.3.1 a key is at least one character"},
		{"empty value", "a=", "§3.3.1.3.2 value = 0*255(chr) nblk-chr"},
		{"uppercase key", "A=1", "§3.3.1.3.1 lcalpha = %x61-7A"},
		{"simple key starting with a digit", "1abc=1", "§3.3.1.3.1 simple-key = lcalpha ..."},
		{"key starting with punctuation", "_a=1", "§3.3.1.3.1 simple-key = lcalpha ..."},
		{"value with a control character", "a=1\x01", "§3.3.1.3.2 chr is %x20-%x7E"},
		{"duplicate key", "a=1,a=2", "§3.3.1.4 only one entry per key is allowed"},
		{"tenant-id too long", strings.Repeat("a", 242) + "@dt=1", "§3.3.1.3.1 tenant-id = ( lcalpha / DIGIT ) 0*240( ... )"},
		{"system-id too long", "tenant@" + strings.Repeat("a", 15) + "=1", "§3.3.1.3.1 system-id = lcalpha 0*13( ... )"},
		{"simple key too long", strings.Repeat("a", 257) + "=1", "§3.3.1.3.1 simple-key = lcalpha 0*255( ... )"},
		{"value too long", "a=" + strings.Repeat("x", 257), "§3.3.1.3.2 value = 0*255(chr) nblk-chr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := coretrace.ParseTraceState(tc.header)
			if !errors.Is(err, coretrace.InvalidTraceState) {
				t.Fatalf("want InvalidTraceState (%s), got %v", tc.rule, err)
			}
			if state.Len() != 0 {
				t.Error("a refused header must produce the empty list, not a salvaged one")
			}
		})
	}
}

// TestParseTraceStateEnforcesTheThirtyTwoMemberCap pins the grammar's own bound:
// `list = list-member 0*31( OWS "," OWS list-member )` is 32 members.
//
// The cap is also what bounds the header's SIZE. With keys capped at 256 and
// values at 256, 32 members is roughly 16 KiB — a bound derived from the
// specification rather than a ceiling this SDK invented, which is why there is no
// knob for it (ADR 0031 §refuse).
func TestParseTraceStateEnforcesTheThirtyTwoMemberCap(t *testing.T) {
	members := make([]string, 0, coretrace.MaxTraceStateMembers+1)
	for i := range coretrace.MaxTraceStateMembers + 1 {
		members = append(members, "k"+strconv.Itoa(i)+"=v")
	}
	t.Run("exactly 32 is accepted", func(t *testing.T) {
		state, err := coretrace.ParseTraceState(strings.Join(members[:coretrace.MaxTraceStateMembers], ","))
		if err != nil {
			t.Fatalf("32 members is the limit, not one past it: %v", err)
		}
		if state.Len() != coretrace.MaxTraceStateMembers {
			t.Errorf("Len = %d, want %d", state.Len(), coretrace.MaxTraceStateMembers)
		}
	})
	t.Run("33 is refused", func(t *testing.T) {
		if _, err := coretrace.ParseTraceState(strings.Join(members, ",")); !errors.Is(err, coretrace.InvalidTraceState) {
			t.Fatalf("want InvalidTraceState, got %v", err)
		}
	})
}

// TestTraceStateInsertMovesTheKeyToTheFront pins §3.5: "modified keys SHOULD be
// moved to the beginning (left) of the list", and "the order of unmodified
// key/value pairs MUST be preserved".
//
// The order is the only information the list carries beyond its values — leftmost
// is the system that touched the trace most recently — which is why this type is
// an ordered slice and not a map.
func TestTraceStateInsertMovesTheKeyToTheFront(t *testing.T) {
	state, err := coretrace.ParseTraceState("a=1,b=2,c=3")
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	updated, err := state.Insert("b", "9")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if got, want := updated.String(), "b=9,a=1,c=3"; got != want {
		t.Errorf("Insert = %q, want %q", got, want)
	}
	if got, want := state.String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("the receiver was mutated: %q, want %q — StateValue is immutable", got, want)
	}
}

// TestTraceStateInsertTruncatesWholeEntriesFromTheRight pins §3.3.1.5's "the
// vendor MUST truncate whole entries", and the choice of which end: the rightmost
// entry is the oldest, and the new one has just been moved to the front.
func TestTraceStateInsertTruncatesWholeEntriesFromTheRight(t *testing.T) {
	members := make([]string, 0, coretrace.MaxTraceStateMembers)
	for i := range coretrace.MaxTraceStateMembers {
		members = append(members, "k"+strconv.Itoa(i)+"=v")
	}
	state, err := coretrace.ParseTraceState(strings.Join(members, ","))
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	updated, err := state.Insert("mine", "x")
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if updated.Len() != coretrace.MaxTraceStateMembers {
		t.Errorf("Len = %d, want the list capped at %d", updated.Len(), coretrace.MaxTraceStateMembers)
	}
	if _, ok := updated.Get("mine"); !ok {
		t.Error("the inserted entry was dropped instead of the oldest one")
	}
	if _, ok := updated.Get("k" + strconv.Itoa(coretrace.MaxTraceStateMembers-1)); ok {
		t.Error("the rightmost (oldest) entry should have been the one truncated")
	}
	if _, ok := updated.Get("k0"); !ok {
		t.Error("truncation must come from the right, so the leftmost entries survive")
	}
}

// TestTraceStateAbsorbsEdgeWhitespaceButInsertRefusesIt pins the ONE place this
// parser is deliberately more lenient than the ABNF, and where the rule is
// enforced instead.
//
// `value = 0*255(chr) nblk-chr` forbids a trailing space, and `list` grants no
// OWS slot before the first member or after the last, so a strictly-positioned
// parser would refuse "a=1 " outright. This one strips OWS around every member,
// edges included, because RFC 7230 §3.2.4 already requires a recipient to strip
// leading and trailing whitespace from a field value BEFORE it is a field-value —
// so the strict check could only ever fire on input the HTTP layer is specified
// to have normalised already, and it would pay for that by discarding another
// vendor's entire list over whitespace nobody can see.
//
// The nblk-chr rule is enforced where it can still prevent something: on the
// WRITE side, in Insert, where a trailing space would be absorbed by the next hop
// and silently change the value this process claimed to set.
func TestTraceStateAbsorbsEdgeWhitespaceButInsertRefusesIt(t *testing.T) {
	state, err := coretrace.ParseTraceState("a=1 ")
	if err != nil {
		t.Fatalf("edge whitespace must be absorbed, not refused: %v", err)
	}
	if value, ok := state.Get("a"); !ok || value != "1" {
		t.Errorf("value = %q (%v), want the trailing space stripped", value, ok)
	}
	var empty coretrace.StateValue
	if _, insertErr := empty.Insert("a", "1 "); !errors.Is(insertErr, coretrace.InvalidTraceState) {
		t.Fatalf("Insert must refuse a value ending in a space, got %v", insertErr)
	}
}

// TestTraceStateInsertRefusesAnUnspellableEntry pins the refusal at the WRITE
// side: an entry the grammar cannot spell would poison every downstream hop,
// where it would be refused as an unparseable header — far from the call that
// wrote it.
func TestTraceStateInsertRefusesAnUnspellableEntry(t *testing.T) {
	cases := []struct{ key, value string }{
		{"Upper", "1"},
		{"ok", "has,comma"},
		{"ok", "has=equals"},
		{"ok", ""},
		{"", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			var empty coretrace.StateValue
			if _, err := empty.Insert(tc.key, tc.value); !errors.Is(err, coretrace.InvalidTraceState) {
				t.Fatalf("want InvalidTraceState, got %v", err)
			}
		})
	}
}

// TestTraceStateDeleteKeepsTheSurroundingOrder pins the third mutation §3.5
// allows, and that it does not disturb the entries around the hole.
func TestTraceStateDeleteKeepsTheSurroundingOrder(t *testing.T) {
	state, err := coretrace.ParseTraceState("a=1,b=2,c=3")
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	if got, want := state.Delete("b").String(), "a=1,c=3"; got != want {
		t.Errorf("Delete = %q, want %q", got, want)
	}
	if got, want := state.Delete("absent").String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("deleting an absent key changed the list: %q, want %q", got, want)
	}
	if got, want := state.String(), "a=1,b=2,c=3"; got != want {
		t.Errorf("the receiver was mutated: %q, want %q", got, want)
	}
}

// TestTraceStateStringDropsOptionalWhitespace pins the rendering: the OWS the
// grammar allows carries no meaning, and omitting it makes the header a
// deterministic function of the list — which is what lets a test compare bytes
// instead of re-parsing.
func TestTraceStateStringDropsOptionalWhitespace(t *testing.T) {
	state, err := coretrace.ParseTraceState("  a=1 ,\tb=2  ")
	if err != nil {
		t.Fatalf("ParseTraceState: %v", err)
	}
	if got, want := state.String(), "a=1,b=2"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	var empty coretrace.StateValue
	if got := empty.String(); got != "" {
		t.Errorf("the empty list renders as %q, want the empty string", got)
	}
}
