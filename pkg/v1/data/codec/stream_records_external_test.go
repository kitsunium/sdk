package codec_test

import (
	stdpem "encoding/pem"
	"io"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/data/codec"
)

// TestNewDecoderStreamsNDJSONAndPEM is #901 through the facade: NDJSON and PEM
// resolve as streaming formats, NDJSON handing one record per Decode with More
// true, true, true, false, and PEM one block per Decode.
func TestNewDecoderStreamsNDJSONAndPEM(t *testing.T) {
	t.Parallel()
	dec, err := codec.NewDecoder(codec.NDJSON, strings.NewReader("{\"n\":1}\n{\"n\":2}\n\n{\"n\":3}\n"))
	if err != nil {
		t.Fatalf("NewDecoder(NDJSON): %v", err)
	}
	var mores []bool
	sum := 0
	for {
		more := dec.More()
		mores = append(mores, more)
		if !more {
			break
		}
		var rec struct{ N int }
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		sum += rec.N
	}
	if sum != 6 || len(mores) != 4 || !mores[0] || !mores[1] || !mores[2] || mores[3] {
		t.Fatalf("records summed to %d, More reported %v", sum, mores)
	}
	pemDec, err := codec.NewDecoder(codec.PEM, strings.NewReader("-----BEGIN A-----\nAAAA\n-----END A-----\n-----BEGIN B-----\n-----END B-----\n"))
	if err != nil {
		t.Fatalf("NewDecoder(PEM): %v", err)
	}
	var types []string
	for pemDec.More() {
		var b *stdpem.Block
		if err := pemDec.Decode(&b); err != nil {
			t.Fatalf("Decode: %v", err)
		}
		types = append(types, b.Type)
	}
	if strings.Join(types, ",") != "A,B" {
		t.Fatalf("PEM stream gave %v", types)
	}
	var b *stdpem.Block
	if err := pemDec.Decode(&b); err != io.EOF {
		t.Fatalf("Decode past the end = %v, want io.EOF", err)
	}
}

// csvOrder is a flat record a CSV export holds.
type csvOrder struct {
	ID    int     `csv:"id" json:"id"`
	Item  string  `csv:"item" json:"item"`
	Price float64 `csv:"price" json:"price"`
}

// TestCSVTypedThroughTheFacade is #902 through the facade: a slice of flat
// structs is written as a table with a header — no longer promoted to one
// JSON cell — read back typed, and streamed one row per Decode; a table the
// facade promoted before #902 (one "_json" cell) still reads back into the
// same slice, through the promotion path.
func TestCSVTypedThroughTheFacade(t *testing.T) {
	t.Parallel()
	orders := []csvOrder{{1, "pen", 1.5}, {2, "ink, blue", 3}}
	data, err := codec.Marshal(codec.CSV, orders)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != "id,item,price\n1,pen,1.5\n2,\"ink, blue\",3\n" {
		t.Fatalf("Marshal wrote %q", data)
	}
	var back []csvOrder
	if err := codec.Unmarshal(codec.CSV, data, &back); err != nil || len(back) != 2 || back[1] != orders[1] {
		t.Fatalf("Unmarshal: %+v, %v", back, err)
	}
	legacy := []byte("_json\n\"[{\"\"id\"\":7,\"\"item\"\":\"\"old\"\",\"\"price\"\":2}]\"\n")
	var old []csvOrder
	if err := codec.Unmarshal(codec.CSV, legacy, &old); err != nil || len(old) != 1 || old[0] != (csvOrder{7, "old", 2}) {
		t.Fatalf("a table promoted before #902: %+v, %v", old, err)
	}
	dec, err := codec.NewDecoder(codec.CSV, strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("NewDecoder(CSV): %v", err)
	}
	n := 0
	for dec.More() {
		var o csvOrder
		if err := dec.Decode(&o); err != nil || o != orders[n] {
			t.Fatalf("row %d: %+v, %v", n, o, err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("streamed %d rows", n)
	}
}
