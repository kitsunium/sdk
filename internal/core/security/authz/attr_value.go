package authz

import "slices"

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

// attrString is AttrString's body: decl_gen.go writes AttrString, from the
// design, as one call of it.
func attrString(key, value string) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindString, text: value}
}

// attrInt64 is AttrInt64's body: decl_gen.go writes AttrInt64, from the
// design, as one call of it.
func attrInt64(key string, value int64) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindInt64, number: value}
}

// attrBool is AttrBool's body: decl_gen.go writes AttrBool, from the
// design, as one call of it.
func attrBool(key string, value bool) AttrValue {
	//: kind is set explicitly so the zero AttrValue stays KindInvalid.
	return AttrValue{key: key, kind: KindBool, flag: value}
}

// attrStrings is AttrStrings's body: decl_gen.go writes AttrStrings, from the
// design, as one call of it.
func attrStrings(key string, values ...string) AttrValue {
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

// stringValue is AttrValue.StringValue's body: decl_gen.go writes AttrValue.StringValue, from the
// design, as one call of it.
func (a AttrValue) stringValue() (value string, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindString {
		//: not text — report the mismatch rather than an empty string.
		return "", false
	}
	//: kind matches; the payload is meaningful.
	return a.text, true
}

// int64Value is AttrValue.Int64Value's body: decl_gen.go writes AttrValue.Int64Value, from the
// design, as one call of it.
func (a AttrValue) int64Value() (value int64, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindInt64 {
		//: not a number — report the mismatch rather than a zero.
		return 0, false
	}
	//: kind matches; the payload is meaningful.
	return a.number, true
}

// boolValue is AttrValue.BoolValue's body: decl_gen.go writes AttrValue.BoolValue, from the
// design, as one call of it.
func (a AttrValue) boolValue() (value, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindBool {
		//: not a flag — report the mismatch rather than a false.
		return false, false
	}
	//: kind matches; the payload is meaningful.
	return a.flag, true
}

// stringsValue is AttrValue.StringsValue's body: decl_gen.go writes AttrValue.StringsValue, from the
// design, as one call of it.
func (a AttrValue) stringsValue() (values []string, ok bool) {
	//: a mismatch returns the zero value AND false, never one or the other.
	if a.kind != KindStrings {
		//: not a set — report the mismatch rather than an empty slice.
		return nil, false
	}
	//: clone on the way out so the attribute stays immutable.
	return slices.Clone(a.list), true
}

// contains is AttrValue.Contains's body: decl_gen.go writes AttrValue.Contains, from the
// design, as one call of it.
func (a AttrValue) contains(want string) (found, ok bool) {
	//: a mismatch returns false AND false, so neither answer can be misread.
	if a.kind != KindStrings {
		//: not a set — report the mismatch rather than "not a member".
		return false, false
	}
	//: scan the backing slice directly; no clone on the read path.
	return slices.Contains(a.list, want), true
}
