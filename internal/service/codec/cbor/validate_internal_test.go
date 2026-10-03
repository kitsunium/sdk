package cbor

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// nested returns n arrays of one element nested inside each other around a
// single integer: 0x81 repeated n times, then 0x01.
func nested(n int, opener byte) []byte {
	return append(bytes.Repeat([]byte{opener}, n), 0x01)
}

// nestedTags returns n tags of number 100 enclosing a single integer.
func nestedTags(n int) []byte {
	return append(bytes.Repeat([]byte{0xd8, 0x64}, n), 0x01)
}

// Test_validateItem pins the length of accepted items and the refusal of
// every kind of item that is not well-formed, not valid, or past a bound.
func Test_validateItem(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		wantLen int
		wantErr string
	}
	tests := []tc{
		{"integer", "1a000f4240", 5, ""},
		{"item followed by more bytes", "0102", 1, ""},
		{"definite map", "a26161016162820203", 9, ""},
		{"indefinite byte string", "5f42010243030405ff", 9, ""},
		{"indefinite text string", "7f657374726561646d696e67ff", 13, ""},
		{"indefinite array of arrays", "9f018202039f0405ffff", 10, ""},
		{"indefinite map", "bf61610161629f0203ffff", 11, ""},
		{"empty indefinite array", "9fff", 2, ""},
		{"self-describe tag", "d9d9f71863", 5, ""},
		{"tag 0 with text", "c074323031332d30332d32315432303a30343a30305a", 22, ""},
		{"tag 1 with float", "c1fb41d452d9ec200000", 10, ""},
		{"tag 2 with indefinite bytes", "c25f4101ff", 5, ""},
		{"tag 256 is not tag 0", "d9010001", 4, ""},
		{"tag 258 is not tag 2", "d9010201", 4, ""},
		{"tag 2^64-1 constrains nothing", "dbffffffffffffffff01", 10, ""},
		{"unassigned simple value is well-formed", "f0", 1, ""},
		{"extended simple value", "f8ff", 2, ""},
		{"text, four-byte code point", "64f0908591", 5, ""},
		{"empty input", "", 0, "ends inside a data item"},
		{"truncated head", "19", 0, "ends inside a data item"},
		{"truncated payload", "43010203"[0:6], 0, "ends inside a data item"},
		{"truncated array", "830102", 0, "ends inside a data item"},
		{"unclosed indefinite array", "9f0102", 0, "ends inside a data item"},
		{"tag without content", "c0", 0, "ends inside a data item"},
		{"reserved 28", "1c", 0, "reserved additional information"},
		{"reserved 30 on a string", "7e", 0, "reserved additional information"},
		{"indefinite unsigned integer", "1f", 0, "indefinite-length marker"},
		{"indefinite negative integer", "3f", 0, "indefinite-length marker"},
		{"indefinite tag", "df01", 0, "indefinite-length marker"},
		{"two-byte simple below 32", "f818", 0, "two-byte simple value below 32"},
		{"break alone", "ff", 0, "break code outside"},
		{"break inside a definite array", "81ff", 0, "break code outside"},
		{"break enclosed by a tag", "9fd864ffff", 0, "break code outside"},
		{"text chunk in a byte string", "5f6161ff", 0, "chunk of an indefinite-length string"},
		{"nested indefinite chunk", "5f5fffff", 0, "chunk of an indefinite-length string"},
		{"integer chunk", "7f01ff", 0, "chunk of an indefinite-length string"},
		{"map closed after a key", "bf01ff", 0, "closed after a key with no value"},
		{"invalid UTF-8", "62fffe", 0, "not valid UTF-8"},
		{"invalid UTF-8 in a skipped value", "a1616161ff", 0, "not valid UTF-8"},
		{"code point split across chunks", "7f61c361a9ff", 0, "not valid UTF-8"},
		{"tag 0 with an integer", "c001", 0, "tag 0, 1, 2 or 3"},
		{"tag 1 with text", "c16161", 0, "tag 0, 1, 2 or 3"},
		{"tag 1 with a boolean", "c1f5", 0, "tag 0, 1, 2 or 3"},
		{"tag 2 with an integer", "c201", 0, "tag 0, 1, 2 or 3"},
		{"array count past the bound", "9a00100001", 0, "array of more than 1048576 elements"},
		{"map count past the bound", "ba00100001", 0, "map of more than 1048576 pairs"},
		{"string length beyond the input", "5bffffffffffffffff", 0, "ends inside a data item"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := hex.DecodeString(tc.data)
		if err != nil {
			t.Fatalf("%s: bad fixture: %v", tc.name, err)
		}
		n, verr := validateItem(data, maxCBORNestedLevels)
		if tc.wantErr == "" {
			if verr != nil || n != tc.wantLen {
				t.Fatalf("%s: validateItem = %d, %v; want %d, nil", tc.name, n, verr, tc.wantLen)
			}
			return
		}
		if verr == nil {
			t.Fatalf("%s: validateItem accepted %x", tc.name, data)
		}
		if !errs.HasReason(verr, "UNMARSHAL_FAILED") || !strings.Contains(errs.PrivateOf(verr), tc.wantErr) {
			t.Errorf("%s: refusal %v (%s), want one mentioning %q", tc.name, verr, errs.PrivateOf(verr), tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_validateItem_depth pins the nesting bound at 32 levels for arrays,
// maps and tags alike, and for an empty container at the 33rd level.
func Test_validateItem_depth(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		data   []byte
		accept bool
	}
	emptyAt33 := append(bytes.Repeat([]byte{0x81}, maxCBORNestedLevels), 0x80)
	tests := []tc{
		{"32 arrays", nested(maxCBORNestedLevels, 0x81), true},
		{"33 arrays", nested(maxCBORNestedLevels+1, 0x81), false},
		{"32 indefinite arrays", append(nested(maxCBORNestedLevels-1, 0x9f)[:maxCBORNestedLevels-1], append([]byte{0x81, 0x01}, bytes.Repeat([]byte{0xff}, maxCBORNestedLevels-1)...)...), true},
		{"32 tags", nestedTags(maxCBORNestedLevels), true},
		{"33 tags", nestedTags(maxCBORNestedLevels + 1), false},
		{"empty array at level 33", emptyAt33, false},
		{"indefinite string at level 33", append(bytes.Repeat([]byte{0x81}, maxCBORNestedLevels), 0x5f, 0xff), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		_, err := validateItem(tc.data, maxCBORNestedLevels)
		if (err == nil) != tc.accept {
			t.Errorf("%s: validateItem err = %v, want accepted %v", tc.name, err, tc.accept)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_validateItem_indefiniteBounds pins the element and chunk bounds of the
// indefinite forms, which no count announces up front.
func Test_validateItem_indefiniteBounds(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		opener  byte
		item    []byte
		count   int
		wantErr string
	}
	tests := []tc{
		{"array at the bound", 0x9f, []byte{0x00}, maxCBORArrayElements, ""},
		{"array past the bound", 0x9f, []byte{0x00}, maxCBORArrayElements + 1, "more than 1048576 elements"},
		{"string chunks past the bound", 0x5f, []byte{0x40}, maxCBORStringChunks + 1, "more than 1048576 chunks"},
		{"map past the bound", 0xbf, []byte{0x00}, 2*maxCBORMapPairs + 1, "more than 1048576 pairs"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data := make([]byte, 0, tc.count*len(tc.item)+2)
		data = append(data, tc.opener)
		data = append(data, bytes.Repeat(tc.item, tc.count)...)
		data = append(data, breakByte)
		_, err := validateItem(data, maxCBORNestedLevels)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: refused at the bound: %v", tc.name, err)
			}
			return
		}
		if err == nil || !strings.Contains(errs.PrivateOf(err), tc.wantErr) {
			t.Errorf("%s: err = %v (%s), want %q", tc.name, err, errs.PrivateOf(err), tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_validator_resume feeds an item one byte longer at a time: the walk
// must report incomplete until the last byte and agree with a single pass,
// which is what lets the stream decoder validate each byte once.
func Test_validator_resume(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
	}
	tests := []tc{
		{"nested definite", "a26161016162820203"},
		{"indefinite everything", "bf61615f42010243030405ff61629f7f6161ff9fffffff"},
		{"long string", "781e" + strings.Repeat("61", 30)},
		{"tags and floats", "d9d9f7c1fb41d452d9ec200000"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := hex.DecodeString(tc.data)
		if err != nil {
			t.Fatalf("%s: bad hex fixture: %v", tc.name, err)
		}
		walk := validator{limit: maxCBORNestedLevels}
		for n := range len(data) {
			complete, err := walk.resume(data[:n])
			if err != nil || complete {
				t.Fatalf("%s: prefix of %d bytes: complete %v, err %v", tc.name, n, complete, err)
			}
		}
		complete, err := walk.resume(data)
		if err != nil || !complete || walk.off != len(data) {
			t.Fatalf("%s: whole item: complete %v, err %v, off %d of %d", tc.name, complete, err, walk.off, len(data))
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tagAdmits covers the content each interpreted tag admits.
func Test_tagAdmits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tag  uint64
		head itemHead
		want bool
	}
	text := itemHead{major: majorText}
	unsigned := itemHead{major: majorUnsigned}
	half := itemHead{major: majorSimple, info: info2Bytes}
	boolean := itemHead{major: majorSimple, info: simpleTrue}
	tests := []tc{
		{"tag 0 text", tagDateTime, text, true},
		{"tag 0 integer", tagDateTime, unsigned, false},
		{"tag 1 integer", tagEpoch, unsigned, true},
		{"tag 1 float", tagEpoch, half, true},
		{"tag 1 boolean", tagEpoch, boolean, false},
		{"tag 2 bytes", tagPositiveBignum, itemHead{major: majorBytes}, true},
		{"tag 3 text", tagNegativeBignum, text, false},
		{"other tag anything", 100, boolean, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if got := tagAdmits(tc.tag, tc.head); got != tc.want {
			t.Errorf("%s: tagAdmits = %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
