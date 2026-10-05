package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Unique declares a unique index: no two entities may share a key, and a
// write that would is refused with a [Conflict] error. An empty key is not
// indexed. [Store].Lookup reads it.
//
//	var Accounts = Service.Store("accounts", Account.Key,
//		kit.Unique("email", func(a Account) string { return a.Email }))
func Unique[T any](name string, key func(T) string) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Unique[T](name, key)
}

// Index declares an index where an entity may have several keys and a key
// several entities — the users a task is shared with. [Store].Find reads it.
// Empty keys are not indexed.
func Index[T any](name string, keys func(T) []string) StoreOption {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Index[T](name, keys)
}
