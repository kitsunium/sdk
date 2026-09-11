package session

import (
	"bytes"
	"encoding/binary"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// frameOrigin is the fixed instant the frames in this file are stamped with.
var frameOrigin = time.Date(2031, 3, 7, 4, 5, 6, 123456789, time.UTC)

// sampleRecord builds a record covering every field the frame carries.
func sampleRecord() record {
	return record{
		digest:         strings.Repeat("ab", 32),
		subject:        "alice@example.test",
		createdAt:      frameOrigin,
		lastSeen:       frameOrigin.Add(time.Minute),
		absoluteExpiry: frameOrigin.Add(time.Hour),
		data:           map[string]string{"locale": "fr", "theme": "dark", "": "empty key"},
	}
}

// TestTheFrameRoundTripsAndIsDeterministic pins both halves of the at-rest
// framing: what goes in comes back, and the same record always produces the
// same bytes.
//
// Determinism is not cosmetic here. It is what lets a test compare frames
// byte-for-byte rather than field-by-field, and it means the sealed record's
// length does not vary with map iteration order.
func TestTheFrameRoundTripsAndIsDeterministic(t *testing.T) {
	t.Parallel()
	original := sampleRecord()
	frame := encodeRecord(original)
	//: the same record, encoded again — and again with a differently-built map.
	shuffled := original
	shuffled.data = maps.Clone(original.data)
	if !bytes.Equal(frame, encodeRecord(shuffled)) {
		t.Error("two encodings of one record produced different bytes")
	}
	decoded, err := decodeRecord(frame)
	if err != nil {
		t.Fatalf("decodeRecord: %v", err)
	}
	if decoded.digest != original.digest || decoded.subject != original.subject {
		t.Errorf("digest/subject = %q/%q, want %q/%q",
			decoded.digest, decoded.subject, original.digest, original.subject)
	}
	//: instants survive as UTC, so a record written under one TZ reads the same
	//: under another.
	if !decoded.createdAt.Equal(original.createdAt) ||
		!decoded.lastSeen.Equal(original.lastSeen) ||
		!decoded.absoluteExpiry.Equal(original.absoluteExpiry) {
		t.Errorf("instants did not round-trip: %+v", decoded)
	}
	if !maps.Equal(decoded.data, original.data) {
		t.Errorf("data = %v, want %v", decoded.data, original.data)
	}
}

// TestAnEmptyPayloadRoundTripsAsNil pins that "no data" stays cheap: a nil map
// encodes to a zero count and decodes back to nil, which ranges as empty and
// needs no guard at any call site.
func TestAnEmptyPayloadRoundTripsAsNil(t *testing.T) {
	t.Parallel()
	bare := record{digest: strings.Repeat("cd", 32), createdAt: frameOrigin}
	decoded, err := decodeRecord(encodeRecord(bare))
	if err != nil {
		t.Fatalf("decodeRecord: %v", err)
	}
	if decoded.data != nil {
		t.Errorf("data = %v, want nil", decoded.data)
	}
}

// TestEveryMalformedFrameIsOneVerdict pins that decode is total: no input
// panics, no input allocates on an attacker-supplied length, and every failure
// is the same RecordCorrupt.
func TestEveryMalformedFrameIsOneVerdict(t *testing.T) {
	t.Parallel()
	valid := encodeRecord(sampleRecord())
	//: a count field claiming four billion entries. Without the bound check in
	//: decodeData this would reserve four billion map slots before discovering
	//: the frame is nine bytes long.
	hostileCount := bytes.Clone(valid)
	countAt := len(hostileCount) - 1
	for offset := range hostileCount {
		//: locate the entry count by re-encoding a record with no data and
		//: taking that frame's length: everything after it is the payload.
		if offset == len(encodeRecord(record{digest: sampleRecord().digest, subject: sampleRecord().subject}))-lenPrefix {
			countAt = offset
			break
		}
	}
	binary.BigEndian.PutUint32(hostileCount[countAt:countAt+lenPrefix], ^uint32(0))
	//: the same four 0xFF bytes read as a STRING length, and as an entry count
	//: on a frame that ends right after it. Both are merely over the cap on a
	//: 64-bit int, but where int is 32 bits int(0xffffffff) is -1, and -1
	//: passed every "> cap" check: the string length reached a slice
	//: expression and panicked, and the count reached make() and a loop that
	//: ran zero times, so a frame claiming four billion entries decoded as an
	//: empty payload. Only a 32-bit run can see it: GOARCH=386 go test.
	//:
	//: Mutation, under GOARCH=386: converting the prefixes to int before the
	//: comparison (the code before the fix) failed with "decodeRecord
	//: panicked: runtime error: slice bounds out of range [5:4]" and with
	//: "decodeRecord = ({... data:map[]}, <nil>), want CodeRecordCorrupt".
	hostileLength := bytes.Clone(valid)
	binary.BigEndian.PutUint32(hostileLength[versionLen:versionLen+lenPrefix], ^uint32(0))
	bareCount := encodeRecord(record{digest: sampleRecord().digest, subject: sampleRecord().subject})
	binary.BigEndian.PutUint32(bareCount[len(bareCount)-lenPrefix:], ^uint32(0))
	tests := []struct {
		name  string
		frame []byte
	}{
		{"empty", nil},
		{"a wrong version", append([]byte{0xFE}, valid[1:]...)},
		{"a truncated header", valid[:5]},
		{"a truncated payload", valid[:len(valid)-3]},
		{"trailing bytes", append(bytes.Clone(valid), 0x00)},
		{"an enormous entry count", hostileCount},
		{"a string length of 0xffffffff", hostileLength},
		{"an entry count of 0xffffffff with nothing after it", bareCount},
		{"noise", bytes.Repeat([]byte{0x7F}, len(valid))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			//: "no input panics" is part of the verdict, so a panic is reported
			//: against its case instead of killing the binary.
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("decodeRecord panicked: %v", recovered)
				}
			}()
			decoded, err := decodeRecord(tc.frame)
			if !errs.HasCode(err, CodeRecordCorrupt) {
				t.Fatalf("decodeRecord = (%+v, %v), want CodeRecordCorrupt", decoded, err)
			}
			if decoded.digest != "" || decoded.data != nil {
				t.Error("a refused frame yielded a partially populated record")
			}
		})
	}
}

// TestThePayloadBoundIsCheckedBeforeTheWorkItFunds pins the caps that keep a
// session store from becoming an unbounded per-request allocation.
func TestThePayloadBoundIsCheckedBeforeTheWorkItFunds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		data    map[string]string
		refused bool
	}{
		{"at the entry cap", generated(maxDataEntries), false},
		{"one entry past the cap", generated(maxDataEntries + 1), true},
		{"a value at the cap", map[string]string{"k": strings.Repeat("x", maxStringLen)}, false},
		{"a value past the cap", map[string]string{"k": strings.Repeat("x", maxStringLen+1)}, true},
		{"a key past the cap", map[string]string{strings.Repeat("k", maxStringLen+1): "v"}, true},
		{"nothing at all", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := boundPayload(tc.data)
			if tc.refused {
				if !errs.HasCode(err, CodePayloadTooLarge) {
					t.Fatalf("boundPayload = %v, want CodePayloadTooLarge", err)
				}
				//: the offending key is caller data and is never named.
				for _, field := range errs.FieldsOf(err) {
					if strings.Contains(field.StringValue(), "x") || strings.Contains(field.StringValue(), "k") {
						t.Errorf("the refusal named caller data: %v", field)
					}
				}
				return
			}
			if err != nil {
				t.Errorf("boundPayload = %v, want nil", err)
			}
		})
	}
}

// generated builds a payload with n distinct keys.
func generated(n int) map[string]string {
	data := make(map[string]string, n)
	for i := range n {
		data[string(rune('a'+i%26))+strings.Repeat("-", i/26+1)] = "v"
	}
	return data
}
