package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
)

// Unique declares a unique index: no two entities may share a key, and a
// write that would is refused with a [Conflict] error. An empty key is not
// indexed. [Store].Lookup reads it.
//
//	var Accounts = Service.Store("accounts", Account.Key,
//		app.Unique("email", func(a Account) string { return a.Email }))
func Unique[T any](name string, key func(T) string) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Unique[T](name, key)
}

// Index declares an index where an entity may have several keys and a key
// several entities — the users a task is shared with. [Store].Find reads it.
// Empty keys are not indexed.
func Index[T any](name string, keys func(T) []string) StoreOption {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Index[T](name, keys)
}
