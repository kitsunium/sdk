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

// entryBudget is the most one connection's entry in the adapter's table may
// take: beside the loop's slot and its timer link, an idle h2c connection
// fits the loop's 256 bytes of Go heap with both tables' growth (#961,
// TestLoopHTTP2_idleMemory in internal/service/net/server). The decoder, the
// table, the held bytes' buffer and the open requests are kept apart, taken
// at the connection's first field block.
const entryBudget = 80

// TestConnHasNoPointer holds the rule of the connection table: Conn, the
// adapter's entry and streamState hold nothing the collector scans, so a slice
// of them does not cost a scan per connection; and an entry stays within
// entryBudget.
func TestConnHasNoPointer(t *testing.T) {
	assertNoPointers(t, reflect.TypeOf(Conn{}), "Conn")
	assertNoPointers(t, reflect.TypeOf(connEntry{}), "connEntry")
	assertNoPointers(t, reflect.TypeOf(connRequests{}), "connRequests")
	assertNoPointers(t, reflect.TypeOf(streamState{}), "streamState")
	assertNoPointers(t, reflect.TypeOf(Decoder{}), "Decoder")
	if size := reflect.TypeOf(connEntry{}).Size(); size > entryBudget {
		t.Fatalf("connEntry is %d bytes, past %d: an idle h2c connection no longer fits the loop's 256 B of Go heap (ADR 0186 §D7, #961)", size, entryBudget)
	}
}
