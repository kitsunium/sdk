package http2

import (
	"reflect"
	"testing"
)

// assertNoPointers holds the rule of a pointer-free connection table: every
// field of t is an integer, a boolean or a nested struct of those.
func assertNoPointers(t *testing.T, typ reflect.Type, path string) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Struct:
		for i := range typ.NumField() {
			f := typ.Field(i)
			assertNoPointers(t, f.Type, path+"."+f.Name)
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return
	default:
		t.Fatalf("%s is %s: the state holds a pointer", path, typ.Kind())
	}
}

// TestConnHasNoPointer holds the rule of the connection table: Conn and
// streamState hold nothing the collector scans, so a slice of them does not
// cost a scan per connection.
func TestConnHasNoPointer(t *testing.T) {
	assertNoPointers(t, reflect.TypeOf(Conn{}), "Conn")
	assertNoPointers(t, reflect.TypeOf(streamState{}), "streamState")
	assertNoPointers(t, reflect.TypeOf(Decoder{}), "Decoder")
	if size := reflect.TypeOf(Conn{}).Size(); size > 256 {
		t.Fatalf("Conn is %d bytes: an idle connection's budget is 256 of Go heap (ADR 0186 §D7)", size)
	}
}
