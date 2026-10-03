// Package sql — hosts the one context key this package stores under.
package sql

// scopeKeyType is the unexported context key type, so no other package can
// collide with it.
type scopeKeyType struct{}

// scopeKey is the single context key this package stores under.
var scopeKey scopeKeyType
