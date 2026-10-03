package cbor_test

import (
	"testing"
)

// element is a struct element whose stale fields a reused slice must not
// keep.
type element struct {
	// A is set by the input.
	A int `cbor:"A"`
	// B is not, and must come back zero.
	B int `cbor:"B"`
}

// TestUnmarshal_bytes pins []byte and [N]byte decoding.
func TestUnmarshal_bytes(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"bytes", "43010203", ptrTo[[]byte](), []byte{1, 2, 3}, ""},
		{"empty bytes are not nil", "40", ptrTo[[]byte](), []byte{}, ""},
		{"empty indefinite bytes are not nil", "5fff", ptrTo[[]byte](), []byte{}, ""},
		{"indefinite bytes", "5f42010243030405ff", ptrTo[[]byte](), []byte{1, 2, 3, 4, 5}, ""},
		{"array of small integers", "83010203", ptrTo[[]byte](), []byte{1, 2, 3}, ""},
		{"array element too large", "8201190100", ptrTo[[]byte](), nil, "of type uint8"},
		{"bignum magnitude", "c2420102", ptrTo[[]byte](), []byte{1, 2}, ""},
		{"text refused", "6161", ptrTo[[]byte](), nil, "a text string into"},
		{"null is nil", "f6", ptrWith([]byte{9}), []byte(nil), ""},
		{"named byte slice", "420102", ptrTo[myBytes](), myBytes{1, 2}, ""},
		{"byte array shorter input", "420102", ptrWith([4]byte{9, 9, 9, 9}), [4]byte{1, 2, 0, 0}, ""},
		{"byte array longer input", "43010203", ptrTo[[2]byte](), [2]byte{1, 2}, ""},
		{"byte array from an array", "820102", ptrTo[[2]byte](), [2]byte{1, 2}, ""},
		{"byte array from a bignum", "c3420102", ptrTo[[2]byte](), [2]byte{1, 2}, ""},
	})
}

// TestUnmarshal_sequences pins slices and arrays.
func TestUnmarshal_sequences(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"slice", "83010203", ptrTo[[]int](), []int{1, 2, 3}, ""},
		{"empty array is not nil", "80", ptrTo[[]int](), []int{}, ""},
		{"indefinite array", "9f010203ff", ptrTo[[]int](), []int{1, 2, 3}, ""},
		{"null is nil", "f6", ptrWith([]int{1}), []int(nil), ""},
		{"reused elements are zeroed", "81a1614101", ptrWith(make([]element, 1, 4)), []element{{A: 1}}, ""},
		{"reused stale element", "81a1614101", func() any { return new([]element{{A: 9, B: 9}}) }, []element{{A: 1}}, ""},
		{"map refused", "a10102", ptrTo[[]int](), nil, "an array or a map into"},
		{"element mismatch is reported, others kept", "8301616103", ptrTo[[]int](), []int{1, 0, 3}, "a text string into"},
		{"array extra elements dropped", "83010203", ptrTo[[2]int](), [2]int{1, 2}, ""},
		{"array missing elements zeroed", "8101", ptrWith([3]int{7, 7, 7}), [3]int{1, 0, 0}, ""},
		{"indefinite array into array", "9f01020304ff", ptrTo[[2]int](), [2]int{1, 2}, ""},
	})
}

// TestUnmarshal_maps pins Go map targets.
func TestUnmarshal_maps(t *testing.T) {
	t.Parallel()
	runTyped(t, []typedCase{
		{"string to int", "a2616101616202", ptrTo[map[string]int](), map[string]int{"a": 1, "b": 2}, ""},
		{"existing entries kept", "a1616101", ptrWith(map[string]int{"old": 9}), map[string]int{"old": 9, "a": 1}, ""},
		{"null is nil", "f6", ptrWith(map[string]int{"old": 9}), map[string]int(nil), ""},
		{"duplicate key: the last wins", "a2616101616102", ptrTo[map[string]int](), map[string]int{"a": 2}, ""},
		{"int keys", "a201020304", ptrTo[map[int]int](), map[int]int{1: 2, 3: 4}, ""},
		{"value mismatch leaves the pair out", "a261610161626161", ptrTo[map[string]int](), map[string]int{"a": 1}, "a text string into"},
		{"key mismatch leaves the pair out", "a20102616103", ptrTo[map[string]int](), map[string]int{"a": 3}, "an unsigned integer into"},
		{
			"untyped values", "a26161016162820203", ptrTo[map[string]any](),
			map[string]any{"a": uint64(1), "b": []any{uint64(2), uint64(3)}},
			"",
		},
		{"untyped values, a key that is not text", "a2616101f5f5", ptrTo[map[string]any](), map[string]any{"a": uint64(1)}, "a boolean into"},
		{"untyped values, null key is empty", "a1f601", ptrTo[map[string]any](), map[string]any{"": uint64(1)}, ""},
		{"string values", "a26161617861626179", ptrTo[map[string]string](), map[string]string{"a": "x", "b": "y"}, ""},
		{"string values, a value that is not text", "a261610161626179", ptrTo[map[string]string](), map[string]string{"b": "y"}, "an unsigned integer into"},
		{"interface keys", "a2616101f501", ptrTo[map[any]int](), map[any]int{"a": 1, true: 1}, ""},
		{"interface key Go cannot hash", "a1810101", ptrTo[map[any]int](), map[any]int{}, "cannot be a Go map key"},
		{"array of interfaces key Go cannot hash", "a18281010203", ptrTo[map[[2]any]int](), map[[2]any]int{}, "cannot be a Go map key"},
		{"indefinite map", "bf616101616202ff", ptrTo[map[string]int](), map[string]int{"a": 1, "b": 2}, ""},
		{"array refused", "820102", ptrTo[map[string]int](), nil, "an array or a map into"},
	})
}
