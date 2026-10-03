// Package validation — one embedded struct encoding/json descends into.
package validation

import "reflect"

// jsonLevel is an embedded struct encoding/json descends into, with the index
// path that reaches it.
type jsonLevel struct {
	typ   reflect.Type
	index []int
}
