// Package config — the schema's declared value: one key and what it holds when
// nobody supplied it.
package config

// DeclaredValue declares the value one configuration key takes when NO Source
// supplied it. It is the domain's answer to the question every configuration
// loader gets wrong: an absent key and a key explicitly set to the zero value
// are different statements, and only one of them may be overridden.
//
// A default is NOT a fallback applied to a zero value after decoding. It is a
// LAYER, merged under every Source before the decode happens, so presence is
// decided while the operator's key still exists as a key. `timeout = 0` in a
// file therefore stays 0, and an omitted `timeout` becomes the default —
// which a post-decode "if zero then default" can never distinguish.
//
// The zero DeclaredValue declares nothing and is refused at construction: an
// empty Key names no configuration.
//
// A key that carries a default is never also REQUIRED. The two are mutually
// exclusive and the contradiction is refused at construction, because a
// required key the schema itself fills can never be missing — the requirement
// would be a clause that cannot fire, and a rule that cannot fire is worse
// than no rule: it is read as protection.
//
// DeclaredValue is a published concrete shape (pkg/v1/config.Default aliases
// it), so ADR 0040 applies: it may still change while the module is v0, said
// out loud, and not after v1.
type DeclaredValue struct {
	// Key is the operator's key, in the dotted grammar
	// core/validation.JoinField produces — "database.max_conns", never
	// "MaxConns". A segment is one level of nesting in the merged map, so the
	// key an author declares here, the key an operator writes in a TOML table,
	// and the path a violation reports are the SAME string. That is the whole
	// reason the grammar is shared rather than reinvented: a message naming
	// something the operator never typed cannot be acted on.
	//
	// A key with an empty segment, a leading or trailing separator, or no
	// counterpart in the target type is refused at construction — a default
	// nothing reads is worse than no default, because it looks configured.
	Key string
	// Value is the typed Go value the key takes when it is absent. It travels
	// the loader's ordinary path — marshalled to JSON with every other layer
	// and decoded by the same round trip — so a default cannot be typed by
	// rules a Source's value is not. A value the round trip cannot carry is
	// refused at construction rather than at the first load that needs it.
	//
	// It is never echoed into an error: a default may be a placeholder
	// credential, and a refusal names the key and the clause instead.
	Value any
}
