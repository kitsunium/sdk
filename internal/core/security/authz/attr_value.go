// Package authz — hosts AttrValue, one typed fact about a request.
package authz

import "slices"

// AttrKind names the type an [AttrValue] carries. The kind is part of the
// attribute's identity, not a hint: a rule comparing strings and an attribute
// holding an integer do not compare — they MISMATCH, and the SDK says so
// rather than quietly answering false (see [AttributeKindMismatch]).
type AttrKind uint8

const (
	// KindInvalid is the zero AttrKind — FIRST in the iota run for exactly
	// that reason. It belongs to the zero [AttrValue] and to nothing else:
	// [NewRequestValue] drops an attribute carrying it, so a kind-less
	// attribute is ABSENT rather than present and unusable.
	KindInvalid AttrKind = iota
	// KindString is a single text value — a department, a tenant, a region.
	KindString
	// KindInt64 is a whole number — an age, a level, a Unix timestamp.
	KindInt64
	// KindBool is a flag — mfa_satisfied, account_locked, is_internal.
	KindBool
	// KindStrings is an unordered set of text values — the roles a subject
	// holds, the groups it belongs to, the scopes a token carries.
	KindStrings
)

// AttrValue is one typed, named fact the caller attaches to a [RequestValue]:
// the subject's department, whether MFA was satisfied, the owner recorded on
// the resource. It is immutable once built, and its accessors report the kind
// mismatch rather than a zero value.
//
// # Why it is typed
//
// The obvious shape for an attribute bag is map[string]string, and it has two
// defects that this domain cannot afford. It cannot express a flag that is
// present and FALSE — "mfa_satisfied=false" and "mfa_satisfied absent" become
// the same empty string — and it makes every numeric comparison a string
// comparison, in which "9" is greater than "10". Both failures resolve toward
// permission often enough to be worth four kinds and an explicit accessor.
type AttrValue struct {
	// key names the attribute. Empty is not a name: NewRequestValue drops it.
	key string
	// kind selects which of the payload members below is meaningful.
	kind AttrKind
	// text carries KindString.
	text string
	// number carries KindInt64.
	number int64
	// flag carries KindBool.
	flag bool
	// list carries KindStrings; it is cloned in and cloned out, so no caller
	// can mutate an attribute another goroutine is evaluating.
	list []string
}

// AttrString builds a text attribute.
func AttrString(key, value string) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindString, text: value}
}

// AttrInt64 builds a whole-number attribute. Timestamps travel here as Unix
// seconds: the domain deliberately has no time kind, because an attribute
// carrying a time zone would make two requests with the same instant compare
// unequal.
func AttrInt64(key string, value int64) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindInt64, number: value}
}

// AttrBool builds a flag attribute. A flag that is present and false is a
// different fact from a flag that is absent, and this is the constructor that
// lets a caller state the first one.
func AttrBool(key string, value bool) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindBool, flag: value}
}

// AttrStrings builds a set attribute — roles, groups, scopes. The slice is
// cloned, so the caller keeps ownership of the one it passed.
//
// AttrStrings(key) with no values is a legitimate attribute: it states that
// the subject holds NO roles, which is a different fact from the caller not
// having said. Rules distinguish the two, and only the second is an error.
func AttrStrings(key string, values ...string) AttrValue {
	//: clone so a later append by the caller cannot rewrite a live attribute.
	return AttrValue{key: key, kind: KindStrings, list: slices.Clone(values)}
}

// Key returns the attribute's name.
func (a AttrValue) Key() string {
	//: direct read of the immutable member.
	return a.key
}

// Kind returns the type the attribute carries.
func (a AttrValue) Kind() AttrKind {
	//: direct read of the immutable member.
	return a.kind
}

// StringValue returns the text payload. ok is false when the attribute is not
// [KindString] — the caller MUST check it: the zero string is a legitimate
// text value, so the returned value alone cannot report a mismatch.
func (a AttrValue) StringValue() (value string, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindString {
		//: not text — report the mismatch rather than an empty string.
		return "", false
	}
	//: kind matches; the payload is meaningful.
	return a.text, true
}

// Int64Value returns the whole-number payload. ok is false when the attribute
// is not [KindInt64].
func (a AttrValue) Int64Value() (value int64, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindInt64 {
		//: not a number — report the mismatch rather than a zero.
		return 0, false
	}
	//: kind matches; the payload is meaningful.
	return a.number, true
}

// BoolValue returns the flag payload. ok is false when the attribute is not
// [KindBool] — and this is the accessor where ignoring ok is most expensive,
// since false is both "the flag is off" and "there is no flag".
func (a AttrValue) BoolValue() (value, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindBool {
		//: not a flag — report the mismatch rather than a false.
		return false, false
	}
	//: kind matches; the payload is meaningful.
	return a.flag, true
}

// StringsValue returns a copy of the set payload. ok is false when the
// attribute is not [KindStrings]. The copy is what makes an AttrValue safe to
// share across goroutines: a caller that sorts or appends to the result cannot
// reach the attribute the next request will evaluate.
func (a AttrValue) StringsValue() (values []string, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindStrings {
		//: not a set — report the mismatch rather than an empty slice.
		return nil, false
	}
	//: clone on the way out so the attribute stays immutable.
	return slices.Clone(a.list), true
}

// Contains reports whether a [KindStrings] attribute holds want. ok is false
// when the attribute is not [KindStrings], which the caller MUST distinguish
// from a set that simply does not contain want.
//
// It exists so membership does not have to allocate: [AttrValue.StringsValue]
// clones, and membership is the single hottest attribute read in the domain —
// every RBAC evaluation performs one per role.
func (a AttrValue) Contains(want string) (found, ok bool) {
	//: a mismatch returns false AND false, so neither answer can be misread.
	if a.kind != KindStrings {
		//: not a set — report the mismatch rather than "not a member".
		return false, false
	}
	//: scan the backing slice directly; no clone on the read path.
	return slices.Contains(a.list, want), true
}
