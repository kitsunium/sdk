// Package i18n — the compile-time proof that [Store] satisfies the port and
// both of its ADR 0039 siblings.
//
// The assertions live in their own file rather than beside the type because a
// port change must fail the build HERE, at a declaration whose only purpose is
// to say what Store claims to be, rather than at whichever call site happened
// to pass one first.
package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/i18n"

// The three interfaces a Store satisfies.
var (
	_ corei18n.Catalog    = (*Store)(nil)
	_ corei18n.KeyLister  = (*Store)(nil)
	_ corei18n.Fallbacker = (*Store)(nil)
)
