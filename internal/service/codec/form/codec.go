// Package form implements application/x-www-form-urlencoded as a
// codec.Codec — the encoding every HTML form POSTs and every query string
// carries. It is stdlib-only: net/url supplies the decode half.
//
// The wire format is a flat, ordered list of key=value pairs joined by '&',
// with keys and values percent-escaped and space written as '+'. It has no
// types, no nesting, and no array syntax: the ONLY way to carry more than one
// value under a key is to repeat the key. This codec models that literally,
// so its native Go shape is url.Values (== map[string][]string). Marshal
// accepts url.Values, map[string][]string and — for the common single-valued
// form — map[string]string, plus non-nil pointers to any of the three;
// Unmarshal writes into a pointer to any of the three.
//
// Repeated keys: "a=1&a=2" decodes to {"a": ["1","2"]}, in wire order. No
// bracket dialect (a[]=, a[0]=) is invented and no value is dropped — see the
// package CLAUDE.md for the full round-trip contract and the two documented
// places where Marshal ∘ Unmarshal is not byte-for-byte identity.
package form

import (
	"bytes"
	"maps"
	"net/url"
	"slices"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Decode bounds and the escaping digit table. Both bounds are compile-time
// constants rather than constructor options ON PURPOSE: ADR 0031 forbids a
// policy whose zero value silently means "inert". A NewWithLimit(0) would be
// exactly that — a caller passing the zero value would disable the guard
// while believing they configured it. With constants there is no zero value
// to misread, and raising a bound is a reviewed source change.
const (
	// maxFormBytes caps a single Unmarshal payload at 8 MiB. Real form
	// bodies are kilobytes; anything past this is either a consumer bug or
	// an attempt to make the decoder allocate on the attacker's behalf.
	maxFormBytes int = 8 * 1024 * 1024

	// maxFormPairs caps the number of key=value pairs a single payload may
	// declare. net/url.ParseQuery has no such limit: "a=1&a=1&…" repeated
	// far enough is a memory-amplification lever, since every pair appends
	// to the same slice. The ceiling is checked from the '&' count BEFORE
	// parsing, so an over-long body is refused without allocating a map.
	maxFormPairs int = 10_000

	// upperhex is the digit table for percent-escapes. Uppercase matches
	// net/url.QueryEscape byte-for-byte — TestAppendQueryEscapeMatchesStdlib
	// pins the equivalence over the full byte range.
	upperhex string = "0123456789ABCDEF"

	// escapedByteWidth is the width of one "%XX" percent-escape, used when
	// pre-sizing the output buffer.
	escapedByteWidth int = 3

	// nibbleShift moves a byte's high nibble into the low position so it
	// can index upperhex.
	nibbleShift byte = 4

	// nibbleMask isolates a byte's low nibble for the same purpose.
	nibbleMask byte = 0x0F
)

// Package-level state: the codec singleton plus the hoisted MIME /
// extension tables.
var (
	//: register the singleton and expose it as a typed package var.
	Codec codec.Codec = codec.Register(&formCodec{})

	//: the one IANA-registered media type for this encoding.
	mimeTypes = []string{"application/x-www-form-urlencoded"}

	//: NEITHER extension is standardised — urlencoded is a wire encoding
	//: for HTTP bodies and query strings, not a file format. They are
	//: registered only so codec.LookupExt has a deterministic answer for
	//: callers who persist a captured body to disk.
	extensions = []string{".form", ".urlencoded"}
)

// formCodec is the concrete Codec implementation for urlencoded forms.
type formCodec struct{}

// New returns the urlencoded form codec instance.
func New() codec.Codec {
	//: stateless — one singleton is enough for the whole process.
	return Codec
}

// Name implements codec.Codec.
func (*formCodec) Name() string {
	//: canonical identifier.
	return "form"
}

// MIMETypes lists every MIME alias.
func (*formCodec) MIMETypes() []string {
	//: hand back a copy so callers cannot mutate the package table.
	return slices.Clone(mimeTypes)
}

// Extensions lists every file extension.
func (*formCodec) Extensions() []string {
	//: hand back a copy so callers cannot mutate the package table.
	return slices.Clone(extensions)
}

// Marshal encodes v as an application/x-www-form-urlencoded body. Keys are
// emitted in ascending lexicographic order — Go map iteration is randomised,
// so sorting is the only way the same value yields the same bytes twice.
// Values under one key keep their slice order, which is the wire order.
func (*formCodec) Marshal(v any) (encoded []byte, err error) {
	//: shape-gate the argument before touching the encoder.
	values, ok := asValues(v)
	//: shape-the-input rejection.
	if !ok {
		//: loud failure — caller passed a shape urlencoded cannot model.
		return nil, valueInvalid("Marshal: argument is not url.Values / map[string][]string / map[string]string")
	}
	//: encode into a freshly sized buffer; encodeInto pre-grows exactly
	//: once so the caller pays a single allocation for the whole body.
	return encodeInto(nil, values), nil
}

// Append encodes v and appends the bytes onto dst. Implements the optional
// codec.Appender interface so hot-path callers (request builders, replay
// harnesses) can reuse one buffer across bodies. On failure dst is returned
// untouched, per the Appender contract.
func (*formCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: same shape gate as Marshal — one contract, two entry points.
	values, ok := asValues(v)
	//: shape-the-input rejection leaves the caller's buffer intact.
	if !ok {
		//: loud failure with dst unchanged.
		return dst, valueInvalid("Append: argument is not url.Values / map[string][]string / map[string]string")
	}
	//: append in place; encodeInto never partially writes on failure
	//: because, past the shape gate, it cannot fail at all.
	return encodeInto(dst, values), nil
}

// Unmarshal parses an urlencoded body into v, which must be a non-nil pointer
// to url.Values, map[string][]string, or map[string]string.
func (*formCodec) Unmarshal(data []byte, v any) error {
	//: refuse oversized bodies before any allocation happens.
	if len(data) > maxFormBytes {
		//: surface the documented sentinel with the offending sizes.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeFormUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "form body exceeds size limit",
			Private: "service/codec/form.Unmarshal: payload length exceeds maxFormBytes",
		}, errs.Int("len", len(data)), errs.Int("cap", maxFormBytes))
	}
	//: '&' count + 1 is an exact upper bound on the pair count, so the
	//: ceiling is enforced without parsing (bytes.Count is SIMD-fast).
	if pairs := bytes.Count(data, []byte{'&'}) + 1; pairs > maxFormPairs {
		//: surface the documented sentinel with the offending counts.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeFormUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "form body declares too many pairs",
			Private: "service/codec/form.Unmarshal: pair count exceeds maxFormPairs",
		}, errs.Int("pairs", pairs), errs.Int("cap", maxFormPairs))
	}
	//: net/url owns the decode half: it defines '+' → space, %XX folding,
	//: and the post-Go-1.17 refusal of ';' as a separator. Re-implementing
	//: those rules here would let this codec drift from r.ParseForm() in
	//: the same process, which is the one thing a form decoder must not do.
	values, perr := url.ParseQuery(string(data))
	//: ParseQuery returns partial values alongside its error; treat any
	//: error as fatal so a half-decoded body never reaches the caller.
	if perr != nil {
		//: wrap the stdlib error for reason-based matching.
		return errs.Wrap(perr, errs.WrapParams{
			Code:    CodeFormUnmarshalFailed,
			Reason:  "UNMARSHAL_FAILED",
			Public:  "form decoding failed",
			Private: "service/codec/form.Unmarshal: net/url.ParseQuery returned an error",
		})
	}
	//: publish through whichever supported pointer shape the caller passed.
	return publish(values, v)
}

// publish writes values through the caller's pointer. The three accepted
// targets differ in fidelity, and the difference is enforced rather than
// smoothed over: url.Values / map[string][]string take the decode verbatim,
// while map[string]string refuses a body that repeats a key.
func publish(values url.Values, v any) error {
	//: dispatch on the concrete target type; anything else is rejected.
	switch dst := v.(type) {
	//: canonical target — the codec's own model.
	case *url.Values:
		//: nil pointer cannot be published through.
		if dst == nil {
			//: loud failure.
			return valueInvalid("Unmarshal: *url.Values target is nil")
		}
		//: hand the decoded map over wholesale.
		*dst = values
		//: nothing to wrap.
		return nil
	//: the unnamed equivalent, accepted so callers need not import net/url.
	case *map[string][]string:
		//: nil pointer cannot be published through.
		if dst == nil {
			//: loud failure.
			return valueInvalid("Unmarshal: *map[string][]string target is nil")
		}
		//: url.Values' underlying type IS map[string][]string — direct assign.
		*dst = values
		//: nothing to wrap.
		return nil
	//: single-valued convenience target — lossy by construction, so it
	//: refuses rather than silently picking a winner among repeated values.
	case *map[string]string:
		//: nil pointer cannot be published through.
		if dst == nil {
			//: loud failure.
			return valueInvalid("Unmarshal: *map[string]string target is nil")
		}
		//: collapse, or refuse if the body repeats a key.
		single, cerr := collapseSingle(values)
		//: surface the MULTI_VALUE refusal untouched.
		if cerr != nil {
			//: already wrapped by collapseSingle.
			return cerr
		}
		//: publish the collapsed map.
		*dst = single
		//: nothing to wrap.
		return nil
	}
	//: anything else is a programming error for this codec.
	return valueInvalid("Unmarshal: target is not *url.Values / *map[string][]string / *map[string]string")
}

// collapseSingle projects values onto a one-value-per-key map, refusing any
// body that repeats a key. The refusal is the point: last-wins and first-wins
// both discard data the wire actually carried, and this codec does not
// pretend a lossy projection succeeded.
func collapseSingle(values url.Values) (single map[string]string, err error) {
	//: exact-size map; every key contributes at most one entry.
	out := make(map[string]string, len(values))
	//: track the lexicographically smallest repeated key so the reported
	//: offender does not depend on Go's randomised map iteration order.
	offender, offenderCount := "", 0
	//: one pass: fill the happy-path entries and note any offender.
	for key, vals := range values {
		//: a repeated key cannot be represented — record and keep scanning.
		if len(vals) > 1 {
			//: keep the smallest key so the error is reproducible.
			if offenderCount == 0 || key < offender {
				//: new minimum offender.
				offender, offenderCount = key, len(vals)
			}
			//: nothing to store for this key.
			continue
		}
		//: ParseQuery never yields an empty slice, but a hand-built
		//: url.Values can; treat it as "key absent" rather than panicking.
		if len(vals) == 0 {
			//: skip the empty binding.
			continue
		}
		//: single value — store it.
		out[key] = vals[0]
	}
	//: a repeated key makes the whole decode fail, not just that entry.
	if offenderCount > 0 {
		//: surface the documented sentinel naming the offending key.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeFormMultiValue,
			Reason:  "MULTI_VALUE",
			Public:  "form key carries multiple values",
			Private: "service/codec/form.Unmarshal: map[string]string target cannot hold a repeated key",
		}, errs.String("key", offender), errs.Int("count", offenderCount))
	}
	//: every key was single-valued.
	return out, nil
}

// asValues coerces v into a url.Values, accepting the three modelled shapes
// plus a non-nil pointer to any of them. map[string]string is widened to one
// value per key, which is lossless in this direction. The pointer forms live
// in asPointedValues so neither half exceeds the branch budget.
func asValues(v any) (values url.Values, ok bool) {
	//: dispatch on the concrete argument type.
	switch src := v.(type) {
	//: canonical shape — no conversion.
	case url.Values:
		//: hand back as-is.
		return src, true
	//: unnamed equivalent — same underlying type, direct conversion.
	case map[string][]string:
		//: convert the header, not the contents.
		return src, true
	//: single-valued convenience shape — widen each entry.
	case map[string]string:
		//: widening costs two allocations total; documented on widenSingle.
		return widenSingle(src), true
	}
	//: pointer forms, accepted for symmetry with Unmarshal's targets.
	return asPointedValues(v)
}

// asPointedValues is asValues' pointer half: it dereferences a non-nil
// pointer to any modelled shape. A nil pointer is rejected rather than
// dereferenced, so a caller's zero-valued variable cannot panic the encoder.
func asPointedValues(v any) (values url.Values, ok bool) {
	//: dispatch on the concrete pointer type.
	switch src := v.(type) {
	//: pointer to the canonical shape.
	case *url.Values:
		//: nil pointer carries no value.
		if src == nil {
			//: reject rather than panic.
			return nil, false
		}
		//: dereference once.
		return *src, true
	//: pointer to the unnamed equivalent.
	case *map[string][]string:
		//: nil pointer carries no value.
		if src == nil {
			//: reject rather than panic.
			return nil, false
		}
		//: dereference once.
		return *src, true
	//: pointer to the single-valued shape.
	case *map[string]string:
		//: nil pointer carries no value.
		if src == nil {
			//: reject rather than panic.
			return nil, false
		}
		//: dereference then widen.
		return widenSingle(*src), true
	}
	//: anything else is a programming error for this codec.
	return nil, false
}

// widenSingle turns a one-value-per-key map into the codec's native
// url.Values shape. The one-element slices share a single backing array, so
// widening costs two allocations in total rather than one per key.
func widenSingle(src map[string]string) url.Values {
	//: exact-size map; one entry per source key.
	out := make(url.Values, len(src))
	//: exact-size backing array, written by index — nothing re-allocates it,
	//: which is what keeps the already-issued sub-slices valid.
	backing := make([]string, len(src))
	//: cursor into the backing array; map order is irrelevant here because
	//: each key keeps a view of the slot written for it.
	slot := 0
	//: one single-element view per key.
	for key, val := range src {
		//: the wire has no notion of "one value" beyond "one repetition".
		backing[slot] = val
		//: three-index slice caps capacity at 1, so a caller appending to
		//: one key's slice cannot overwrite the next key's element.
		out[key] = backing[slot : slot+1 : slot+1]
		//: advance to the next slot.
		slot++
	}
	//: caller owns the widened map.
	return out
}

// encodeInto appends the canonical urlencoded rendering of values onto dst
// and returns the grown buffer. Byte-for-byte identical to
// url.Values.Encode() — pinned by TestEncodeIntoMatchesStdlibEncode — but
// writes into a caller-owned []byte instead of minting a string, which is
// what makes Append worth having.
func encodeInto(dst []byte, values url.Values) []byte {
	//: an empty map encodes to the empty body; nothing to append.
	if len(values) == 0 {
		//: leave dst exactly as the caller handed it over.
		return dst
	}
	//: collect the keys into an exactly-sized slice — slices.Collect would
	//: grow geometrically from nil and cost three allocations where one does.
	keys := slices.AppendSeq(make([]string, 0, len(values)), maps.Keys(values))
	//: ascending lexicographic order is the canonical form this codec emits;
	//: Go map order is randomised, so sorting is the only way the same value
	//: encodes to the same bytes twice.
	slices.Sort(keys)
	//: one grow for the whole body — the exact length is computable.
	dst = slices.Grow(dst, encodedLen(values, keys))
	//: '&' precedes every pair except the first WRITTEN one; a key bound
	//: to an empty slice writes nothing, so a counter beats index math.
	written := 0
	//: emit key=value for every value under every key, in sorted key order.
	for _, key := range keys {
		//: values keep their slice order, which is their wire order.
		for _, val := range values[key] {
			//: separator before every pair but the first.
			if written > 0 {
				//: pair separator.
				dst = append(dst, '&')
			}
			//: escape the key, then the '=' , then the value.
			dst = appendQueryEscape(dst, key)
			dst = append(dst, '=')
			dst = appendQueryEscape(dst, val)
			//: count what actually reached the buffer.
			written++
		}
	}
	//: caller owns the grown buffer.
	return dst
}

// encodedLen computes the exact byte length encodeInto will append, so the
// buffer is grown once instead of doubling its way there.
func encodedLen(values url.Values, keys []string) int {
	//: running payload total, separators added at the end.
	total := 0
	//: pair count drives the separator arithmetic.
	pairs := 0
	//: sum key + '=' + value for every emitted pair.
	for _, key := range keys {
		//: the key is escaped once per repetition, so measure it once.
		keyLen := escapedLen(key)
		//: every value under this key produces one pair.
		for _, val := range values[key] {
			//: key + '=' + value.
			total += keyLen + 1 + escapedLen(val)
			//: one more pair to separate.
			pairs++
		}
	}
	//: n pairs need n-1 '&' separators; zero pairs need none.
	if pairs > 0 {
		//: add the separators.
		total += pairs - 1
	}
	//: exact byte count of the appended body.
	return total
}

// escapedLen reports how many bytes appendQueryEscape will write for s.
func escapedLen(s string) int {
	//: every byte contributes at least one output byte.
	total := len(s)
	//: each escaped byte costs two MORE than the one already counted.
	for i := range len(s) {
		//: space becomes '+' — same width, nothing to add.
		if s[i] == ' ' || isUnreservedQueryByte(s[i]) {
			//: single-byte output.
			continue
		}
		//: "%XX" is three bytes where one was already counted.
		total += escapedByteWidth - 1
	}
	//: exact output width.
	return total
}

// appendQueryEscape appends s to dst using the query-component escaping rules
// of net/url.QueryEscape: RFC 3986 §2.3 unreserved bytes travel verbatim,
// space becomes '+', and every other byte becomes an uppercase "%XX" triple.
// Hand-rolled because QueryEscape returns a string, which would put one
// allocation per key and per value on the encode path.
func appendQueryEscape(dst []byte, s string) []byte {
	//: byte-wise walk — the rules are defined on bytes, not runes, so a
	//: multi-byte rune is escaped one byte at a time exactly as stdlib does.
	for i := range len(s) {
		//: current input byte.
		c := s[i]
		//: the query-component special case: space is '+', not "%20".
		if c == ' ' {
			//: single-byte output.
			dst = append(dst, '+')
			//: next input byte.
			continue
		}
		//: unreserved bytes are safe verbatim.
		if isUnreservedQueryByte(c) {
			//: passthrough.
			dst = append(dst, c)
			//: next input byte.
			continue
		}
		//: everything else percent-escapes with UPPERCASE hex digits.
		dst = append(dst, '%', upperhex[c>>nibbleShift], upperhex[c&nibbleMask])
	}
	//: caller owns the grown buffer.
	return dst
}

// isUnreservedQueryByte reports whether c is an RFC 3986 §2.3 unreserved
// byte, i.e. one net/url.QueryEscape leaves untouched.
func isUnreservedQueryByte(c byte) bool {
	//: the unreserved set is exactly alphanumerics plus the four marks.
	return isASCIIAlphanumeric(c) || isUnreservedMark(c)
}

// isASCIIAlphanumeric reports whether c is an ASCII letter or digit.
func isASCIIAlphanumeric(c byte) bool {
	//: three contiguous ranges, no table lookup needed.
	return 'a' <= c && c <= 'z' ||
		'A' <= c && c <= 'Z' ||
		'0' <= c && c <= '9'
}

// isUnreservedMark reports whether c is one of the four RFC 3986 §2.3
// unreserved punctuation marks.
func isUnreservedMark(c byte) bool {
	//: the mark set is closed and tiny.
	return c == '-' || c == '_' || c == '.' || c == '~'
}

// valueInvalid builds the shape-rejection error with a call-site-specific
// Private detail. Centralised so every entry point emits the same Code /
// Reason / Public triple.
func valueInvalid(detail string) error {
	//: one sentinel shape, one construction site.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeFormValueInvalid,
		Reason:  "VALUE_INVALID",
		Public:  "form codec requires a url.Values-shaped value",
		Private: "service/codec/form." + detail,
	})
}
