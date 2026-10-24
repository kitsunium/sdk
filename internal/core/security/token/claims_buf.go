package token

import (
	"bytes"
	"slices"
	"strconv"
	"unicode/utf8"
)

// The fixed capacities of a [ClaimsBuf]. They are bounds, not budgets: a claim
// longer than its slot, an audience with more members, or more declared
// private names than this is refused with [ClaimsBufOverflow] — never cut to
// fit, because a truncated subject is another subject.
const (
	// ClaimsBufTextCap is the capacity, in octets, of every text slot of a
	// ClaimsBuf: each registered string claim, each audience member and each
	// declared private claim, decoded.
	ClaimsBufTextCap int = 256
	// ClaimsBufAudienceCap is how many "aud" members a ClaimsBuf holds.
	ClaimsBufAudienceCap int = 4
	// ClaimsBufPrivateCap is how many private claim names a ClaimsBuf may
	// declare.
	ClaimsBufPrivateCap int = 8
)

const (
	// textClaims is how many TextClaim values exist.
	textClaims int = 3
	// timeClaims is how many TimeClaim values exist.
	timeClaims int = 3
)

// ClaimState is what a [ClaimsBuf] knows about one claim after a
// verification: absent, of the wrong JSON type, present but invalid, or
// present and usable. The zero value is [ClaimAbsent].
type ClaimState uint8

const (
	// ClaimAbsent is the state of a claim the token did not carry — and of
	// every claim of a ClaimsBuf that was reset or whose verification failed.
	ClaimAbsent ClaimState = iota
	// ClaimWrongType is the state of a declared private claim whose JSON type
	// is not a string. A registered claim of the wrong type is never stored:
	// the token is refused with [Malformed].
	ClaimWrongType
	// ClaimInvalid is the state of a text claim that is the empty string.
	ClaimInvalid
	// ClaimOK is the state of a claim present with a usable value.
	ClaimOK
)

// TextClaim names one registered claim a [ClaimsBuf] stores as text.
type TextClaim uint8

const (
	// TextIssuer is the "iss" claim.
	TextIssuer TextClaim = iota
	// TextSubject is the "sub" claim.
	TextSubject
	// TextID is the "jti" claim.
	TextID
)

// TimeClaim names one registered claim a [ClaimsBuf] stores as an instant.
type TimeClaim uint8

const (
	// TimeExpiry is the "exp" claim.
	TimeExpiry TimeClaim = iota
	// TimeNotBefore is the "nbf" claim.
	TimeNotBefore
	// TimeIssuedAt is the "iat" claim.
	TimeIssuedAt
)

// textSlot is one fixed-capacity text claim.
type textSlot struct {
	// value holds the decoded octets; only the first n are meaningful.
	value [ClaimsBufTextCap]byte
	// n is how many octets of value the claim occupies.
	n uint16
	// state is what is known about the claim.
	state ClaimState
}

// timeSlot is one registered instant, as Unix seconds and nanoseconds.
type timeSlot struct {
	// sec is the whole seconds since the Unix epoch.
	sec int64
	// nsec is the fraction past sec, in nanoseconds, in [0, 1e9): sec is the
	// floor, as time.Time.Unix reports it.
	nsec int32
	// state is what is known about the claim.
	state ClaimState
}

// ClaimsBuf is a caller-owned, fixed-capacity claim set that a
// [VerifierInto] fills without allocating: one per request, reused from one
// request to the next. Registered claims are stored as decoded bytes or Unix
// seconds; private claims are stored only when the caller declared their name
// at construction ([NewClaimsBuf]), decoded, and only when they are JSON
// strings. Every field carries a [ClaimState].
//
// Nothing in it aliases the token or the memory it was received in: every
// value is a copy. A verification overwrites the whole buffer on entry and
// leaves it reset when it fails, so no byte of a previous request survives
// into the next; the declared names are configuration and are kept.
//
// The views its accessors return are slices of the buffer itself, valid until
// the next verification or [ClaimsBuf.Reset]. String and GoString render the
// SHAPE of the buffer, never a value. It is not safe for concurrent use.
type ClaimsBuf struct {
	// text holds iss, sub and jti, indexed by TextClaim.
	text [textClaims]textSlot
	// times holds exp, nbf and iat, indexed by TimeClaim.
	times [timeClaims]timeSlot
	// audience holds the "aud" members, in token order.
	audience [ClaimsBufAudienceCap]textSlot
	// audienceLen is how many audience entries are populated.
	audienceLen uint8
	// names are the declared private claim names, configuration kept across
	// resets.
	names [ClaimsBufPrivateCap]string
	// declared is how many entries of names are populated.
	declared uint8
	// private holds the declared private claims, indexed like names.
	private [ClaimsBufPrivateCap]textSlot
}

// String reports the state's name: "absent", "wrong_type", "invalid" or "ok".
func (s ClaimState) String() string {
	//: closed switch — every state has one spelling.
	switch s {
	//: the zero value.
	case ClaimAbsent:
		//: not carried.
		return "absent"
	//: present, not a string.
	case ClaimWrongType:
		//: wrong JSON type.
		return "wrong_type"
	//: present, empty.
	case ClaimInvalid:
		//: present but unusable.
		return "invalid"
	//: present and usable.
	case ClaimOK:
		//: usable.
		return "ok"
	//: any out-of-range value.
	default:
		//: a name no state uses.
		return "unknown"
	}
}

// NewClaimsBuf returns an empty buffer declaring the private claim names
// private, in order. It refuses an empty name, a registered one, a repeated
// one, or one a token could only spell with an escape — a quote, a backslash,
// a control character or bytes that are not UTF-8 — with [ClaimNameInvalid],
// and a name longer than [ClaimsBufTextCap] or more than [ClaimsBufPrivateCap]
// names with [ClaimsBufOverflow].
func NewClaimsBuf(private ...string) (buf *ClaimsBuf, err error) {
	//: the cap is a bound on the declaration, checked before any name.
	if len(private) > ClaimsBufPrivateCap {
		//: refuse rather than silently drop the extra names.
		return nil, ClaimsBufOverflow
	}
	buf = &ClaimsBuf{}
	//: each name is checked against the rules and the names before it.
	for _, name := range private {
		//: one refusal, the first, in declaration order.
		if derr := buf.declare(name); derr != nil {
			//: no half-declared buffer is returned.
			return nil, derr
		}
	}
	//: declared and empty.
	return buf, nil
}

// declare appends one private claim name to b's declaration.
func (b *ClaimsBuf) declare(name string) error {
	//: a name past the slot capacity could never be stored as a key.
	if len(name) > ClaimsBufTextCap {
		//: a bound, named as one.
		return ClaimsBufOverflow
	}
	//: the empty, the registered and the unspellable are not claim names, and
	//: a repeated name would be two slots for one claim.
	if name == "" || IsRegisteredClaim(name) || !plainMemberName(name) ||
		slices.Contains(b.names[:b.declared], name) {
		//: the same verdict WithPrivateRaw gives.
		return ClaimNameInvalid
	}
	b.names[b.declared] = name
	b.declared++
	//: declared.
	return nil
}

// plainMemberName reports whether name is UTF-8 that a JSON member name
// carries without an escape: no quote, no backslash, no control character.
func plainMemberName(name string) bool {
	//: a verifier refuses every escaped member name, so a name that needs
	//: one could never match.
	for i := range len(name) {
		//: the three byte classes JSON would have to escape.
		if c := name[i]; c == '"' || c == '\\' || c < ' ' {
			//: unspellable without an escape.
			return false
		}
	}
	//: and a verifier refuses every claims object that is not UTF-8.
	return utf8.ValidString(name)
}

// Reset clears every value and state of b, keeping its declared names.
func (b *ClaimsBuf) Reset() {
	//: every value octet, not only the lengths: nothing survives a reset.
	clear(b.text[:])
	clear(b.times[:])
	clear(b.audience[:])
	b.audienceLen = 0
	clear(b.private[:])
}

// Text returns a view of the registered text claim c and its state. The view
// is nil unless the state is [ClaimOK].
func (b *ClaimsBuf) Text(c TextClaim) (value []byte, state ClaimState) {
	//: an out-of-range claim is absent.
	if int(c) >= textClaims {
		//: no such slot.
		return nil, ClaimAbsent
	}
	//: the slot's view.
	return b.text[c].view()
}

// SetText stores value, decoded, as the registered text claim c. The empty
// value is stored as [ClaimInvalid]. A value longer than [ClaimsBufTextCap] is
// refused with [ClaimsBufOverflow] and leaves the claim absent. It is the
// writer a [VerifierInto] implementation fills the buffer with.
func (b *ClaimsBuf) SetText(c TextClaim, value []byte) error {
	//: an out-of-range claim has no slot to overflow into.
	if int(c) >= textClaims {
		//: refuse.
		return ClaimsBufOverflow
	}
	//: the slot decides fit and state.
	return b.text[c].set(value)
}

// Unix returns the registered instant c as Unix seconds and nanoseconds, and
// its state. Both numbers are zero unless the state is [ClaimOK].
func (b *ClaimsBuf) Unix(c TimeClaim) (sec int64, nsec int32, state ClaimState) {
	//: an out-of-range claim is absent.
	if int(c) >= timeClaims {
		//: no such slot.
		return 0, 0, ClaimAbsent
	}
	slot := b.times[c]
	//: the instant as stored.
	return slot.sec, slot.nsec, slot.state
}

// SetUnix stores the registered instant c as Unix seconds and nanoseconds, in
// the [ClaimOK] state. It is the writer a [VerifierInto] implementation fills
// the buffer with; an out-of-range c is ignored.
func (b *ClaimsBuf) SetUnix(c TimeClaim, sec int64, nsec int32) {
	//: an out-of-range claim has no slot.
	if int(c) >= timeClaims {
		//: nothing to store.
		return
	}
	b.times[c] = timeSlot{sec: sec, nsec: nsec, state: ClaimOK}
}

// AudienceLen reports how many "aud" members b holds.
func (b *ClaimsBuf) AudienceLen() int {
	//: the populated prefix.
	return int(b.audienceLen)
}

// Audience returns a view of the i-th "aud" member and its state; out of
// range, it is absent.
func (b *ClaimsBuf) Audience(i int) (value []byte, state ClaimState) {
	//: only the populated prefix is readable.
	if i < 0 || i >= int(b.audienceLen) {
		//: no such member.
		return nil, ClaimAbsent
	}
	//: the member's view.
	return b.audience[i].view()
}

// AppendAudience stores value, decoded, as the next "aud" member. A member
// past [ClaimsBufAudienceCap], or longer than [ClaimsBufTextCap], is refused
// with [ClaimsBufOverflow]. It is the writer a [VerifierInto] implementation
// fills the buffer with.
func (b *ClaimsBuf) AppendAudience(value []byte) error {
	//: the member count is a bound like the octet count.
	if int(b.audienceLen) >= ClaimsBufAudienceCap {
		//: refuse rather than drop a member.
		return ClaimsBufOverflow
	}
	//: the slot decides fit and state.
	if err := b.audience[b.audienceLen].set(value); err != nil {
		//: the member did not fit.
		return err
	}
	b.audienceLen++
	//: appended.
	return nil
}

// Declared reports how many private claim names b declares.
func (b *ClaimsBuf) Declared() int {
	//: the populated prefix.
	return int(b.declared)
}

// PrivateIndex returns the declaration index of the private claim name, or -1
// when b does not declare it. name is the member name as the token spells it.
func (b *ClaimsBuf) PrivateIndex(name []byte) int {
	//: at most ClaimsBufPrivateCap names: a scan beats any index.
	for i, declared := range b.names[:b.declared] {
		//: the conversion does not escape, so it is zero-copy: no allocation.
		if bytes.Equal(name, []byte(declared)) {
			//: declared here.
			return i
		}
	}
	//: not declared.
	return -1
}

// Private returns a view of the declared private claim name and its state. A
// name b does not declare is reported absent.
func (b *ClaimsBuf) Private(name string) (value []byte, state ClaimState) {
	//: the same scan PrivateIndex runs.
	for i, declared := range b.names[:b.declared] {
		//: compared exactly.
		if declared == name {
			//: the slot's view.
			return b.private[i].view()
		}
	}
	//: not declared, so never stored.
	return nil, ClaimAbsent
}

// SetPrivate stores value, decoded, as the private claim declared at index i.
// The empty value is stored as [ClaimInvalid]; a value longer than
// [ClaimsBufTextCap] is refused with [ClaimsBufOverflow] and leaves the claim
// absent. It is the writer a [VerifierInto] implementation fills the buffer
// with; an index b does not declare is refused with [ClaimsBufOverflow].
func (b *ClaimsBuf) SetPrivate(i int, value []byte) error {
	//: only a declared index has a slot.
	if i < 0 || i >= int(b.declared) {
		//: no such slot.
		return ClaimsBufOverflow
	}
	//: the slot decides fit and state.
	return b.private[i].set(value)
}

// MarkPrivate sets the state of the private claim declared at index i without
// a value — [ClaimWrongType] for a member that is not a JSON string. It is the
// writer a [VerifierInto] implementation fills the buffer with; an index b does
// not declare is ignored.
func (b *ClaimsBuf) MarkPrivate(i int, state ClaimState) {
	//: only a declared index has a slot.
	if i < 0 || i >= int(b.declared) {
		//: nothing to mark.
		return
	}
	b.private[i] = textSlot{state: state}
}

// String implements fmt.Stringer and renders the SHAPE of b, never a value:
// "token.ClaimsBuf{registered:3 aud:1 private:1/2}" — the registered claims
// that are usable, the audience members, and the usable private claims over
// the declared ones. Like a [ClaimsValue], a ClaimsBuf holds the output of an
// authentication decision, and %v is how such output reaches a log. The
// receiver is a value, so a ClaimsBuf formatted by value is redacted too.
func (b ClaimsBuf) String() string {
	registered := 0
	//: the three text claims.
	for i := range b.text {
		//: count the usable ones, naming none.
		if b.text[i].state == ClaimOK {
			registered++
		}
	}
	//: the three instants.
	for i := range b.times {
		//: count the usable ones, naming none.
		if b.times[i].state == ClaimOK {
			registered++
		}
	}
	usable := 0
	//: the declared private claims.
	for i := range b.private[:b.declared] {
		//: count the usable ones, naming none — not even the declared names.
		if b.private[i].state == ClaimOK {
			usable++
		}
	}
	//: counts only.
	return "token.ClaimsBuf{registered:" + strconv.Itoa(registered) +
		" aud:" + strconv.Itoa(int(b.audienceLen)) +
		" private:" + strconv.Itoa(usable) + "/" + strconv.Itoa(int(b.declared)) + "}"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump every slot.
func (b ClaimsBuf) GoString() string {
	//: same shape-only rendering as String.
	return b.String()
}

// view returns the slot's octets when usable, and its state.
func (s *textSlot) view() (value []byte, state ClaimState) {
	//: only a usable claim has a value to show.
	if s.state != ClaimOK {
		//: the state alone.
		return nil, s.state
	}
	//: a view of the stored octets, capped so an append cannot reach past.
	return s.value[:s.n:s.n], ClaimOK
}

// set copies value into the slot, or refuses it when it does not fit.
func (s *textSlot) set(value []byte) error {
	//: never truncate: a shorter claim is another claim.
	if len(value) > ClaimsBufTextCap {
		//: the slot is left absent.
		*s = textSlot{}
		//: a bound, named as one.
		return ClaimsBufOverflow
	}
	//: the empty string is present but unusable.
	if len(value) == 0 {
		*s = textSlot{state: ClaimInvalid}
		//: stored as invalid, not refused.
		return nil
	}
	s.n = uint16(copy(s.value[:], value))
	//: clear what a longer previous value left past the new one.
	clear(s.value[s.n:])
	s.state = ClaimOK
	//: stored.
	return nil
}
