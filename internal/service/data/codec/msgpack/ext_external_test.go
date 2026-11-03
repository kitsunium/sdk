package msgpack_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// extHolder carries an application extension as a struct field.
type extHolder struct {
	E msgpack.Ext  `msgpack:"e"`
	P *msgpack.Ext `msgpack:"p"`
}

// TestExtRoundTrip is #903: an application extension (types 0–127, and any
// other than the timestamp's −1) used to fail a whole document decoded into
// an interface and could not be written at all. It decodes as an Ext — the
// type and a copy of the payload — and an Ext encodes in the shortest form
// its length allows.
func TestExtRoundTrip(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	var got any
	if err := c.Unmarshal(fromHex(t, "d4 05 2a"), &got); err != nil {
		t.Fatalf("Unmarshal d4 05 2a: %v", err)
	}
	ext, ok := got.(msgpack.Ext)
	if !ok || ext.Type != 5 || !bytes.Equal(ext.Data, []byte{0x2a}) {
		t.Fatalf("decoded %#v, want msgpack.Ext{Type:5, Data:[0x2a]}", got)
	}
	for _, tc := range []struct {
		name string
		ext  msgpack.Ext
		head string
	}{
		{"fixext 1", msgpack.Ext{Type: 5, Data: []byte{0x2a}}, "d4 05"},
		{"fixext 2", msgpack.Ext{Type: 0, Data: make([]byte, 2)}, "d5 00"},
		{"fixext 4", msgpack.Ext{Type: 127, Data: make([]byte, 4)}, "d6 7f"},
		{"fixext 8", msgpack.Ext{Type: 1, Data: make([]byte, 8)}, "d7 01"},
		{"fixext 16", msgpack.Ext{Type: 2, Data: make([]byte, 16)}, "d8 02"},
		{"ext 8, empty", msgpack.Ext{Type: 3}, "c7 00 03"},
		{"ext 8", msgpack.Ext{Type: 3, Data: make([]byte, 3)}, "c7 03 03"},
		{"ext 16", msgpack.Ext{Type: -2, Data: make([]byte, 300)}, "c8 01 2c fe"},
		{"ext 32", msgpack.Ext{Type: 9, Data: make([]byte, 70_000)}, "c9 00 01 11 70 09"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := c.Marshal(tc.ext)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			head := fromHex(t, tc.head)
			if !bytes.HasPrefix(data, head) || len(data) != len(head)+len(tc.ext.Data) {
				t.Fatalf("Marshal wrote % x…, want % x then %d payload bytes", data[:min(len(data), 8)], head, len(tc.ext.Data))
			}
			var back msgpack.Ext
			if err := c.Unmarshal(data, &back); err != nil || back.Type != tc.ext.Type || !bytes.Equal(back.Data, tc.ext.Data) {
				t.Fatalf("typed round trip: %#v, %v", back, err)
			}
		})
	}
}

// TestExtInContainers decodes an Ext inside a document, into a struct field
// and through a pointer, and keeps the timestamp a time.Time.
func TestExtInContainers(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	in := extHolder{E: msgpack.Ext{Type: 7, Data: []byte("abc")}, P: &msgpack.Ext{Type: 8, Data: []byte{1}}}
	data, err := c.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var back extHolder
	if err := c.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.E.Type != 7 || string(back.E.Data) != "abc" || back.P == nil || back.P.Type != 8 || !bytes.Equal(back.P.Data, []byte{1}) {
		t.Fatalf("decoded %#v", back)
	}
	var doc map[string]any
	if err := c.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal into a document: %v", err)
	}
	if e, ok := doc["e"].(msgpack.Ext); !ok || e.Type != 7 {
		t.Fatalf("document member e is %#v", doc["e"])
	}
	stamp, err := c.Marshal(time.Unix(1, 0))
	if err != nil {
		t.Fatalf("Marshal time: %v", err)
	}
	var asAny any
	if err := c.Unmarshal(stamp, &asAny); err != nil {
		t.Fatalf("Unmarshal timestamp: %v", err)
	}
	if _, isTime := asAny.(time.Time); !isTime {
		t.Fatalf("the timestamp extension decoded as %T, want time.Time", asAny)
	}
	var raw msgpack.Ext
	if err := c.Unmarshal(stamp, &raw); err != nil || raw.Type != -1 || len(raw.Data) != 4 {
		t.Fatalf("a timestamp into an Ext: %#v, %v", raw, err)
	}
	var notExt msgpack.Ext
	if err := c.Unmarshal(fromHex(t, "a1 61"), &notExt); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("a str into an Ext: want UNMARSHAL_FAILED, got %v", err)
	}
	if err := c.Unmarshal(fromHex(t, "c0"), &notExt); err != nil || notExt.Type != 0 || notExt.Data != nil {
		t.Fatalf("nil into an Ext: %#v, %v", notExt, err)
	}
}
