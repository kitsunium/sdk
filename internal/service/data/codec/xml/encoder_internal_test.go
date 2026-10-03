package xml

import (
	"bytes"
	stdxml "encoding/xml"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

type xmlDoc struct {
	XMLName stdxml.Name `xml:"doc"`
	N       int         `xml:"n"`
}

// Test_xmlEncoder_Encode exercises the Encode wrapper against both happy-path
// and failure inputs so the error-case branch is covered.
func Test_xmlEncoder_Encode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		value   any
		wantErr bool
	}
	tests := []tc{
		{"named struct encodes cleanly", xmlDoc{N: 1}, false},
		{"channel triggers failure", make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &xmlEncoder{inner: stdxml.NewEncoder(&bytes.Buffer{})}
		err := enc.Encode(tc.value)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Encode err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_xmlEncoder_Close exercises the Close wrapper after a successful Encode.
func Test_xmlEncoder_Close(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		wantErr bool
	}
	tests := []tc{
		{"close after encode succeeds", false},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &xmlEncoder{inner: stdxml.NewEncoder(&bytes.Buffer{})}
		if err := enc.Encode(xmlDoc{N: 1}); err != nil {
			t.Fatalf("%s: Encode setup err=%v", tc.name, err)
		}
		err := enc.Close()
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Close err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// failingWriter always fails its Write so the stdlib encoder's Flush (driven
// by Close) surfaces an error. Used to reach the Close wrap branch.
type failingWriter struct{}

// Write reports a synthetic failure for every byte the encoder flushes.
func (failingWriter) Write(_ []byte) (n int, err error) {
	//: deterministic failure so Flush propagates an error through Close.
	return 0, errors.New("synthetic writer failure")
}

// Test_xmlEncoder_Close_FlushError covers the Close wrap branch
// (encoder.go:34) which the success-only test never reaches. The trick is
// to leave a token buffered without flushing it: EncodeToken does NOT flush
// per call, so a StartElement stays pending until Close → Flush, which then
// fails over a writer that errors on every byte. Calling Encode instead
// would flush eagerly and leave nothing for Close to push, so the failing
// writer must be paired with a buffered-token producer.
func Test_xmlEncoder_Close_FlushError(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"flush failure surfaces via Close wrap"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		enc := &xmlEncoder{inner: stdxml.NewEncoder(failingWriter{})}
		//: a start-element token stays buffered (EncodeToken does not flush),
		//: so the pending bytes are pushed only when Close calls Flush.
		if terr := enc.inner.EncodeToken(stdxml.StartElement{Name: stdxml.Name{Local: "x"}}); terr != nil {
			t.Fatalf("%s: EncodeToken setup err=%v", tc.name, terr)
		}
		//: Close → Flush fails over the failing writer, hitting the wrap.
		//: assert the typed code, not just non-nil, so a regression where
		//: Close stops carrying the dotted-quad sentinel actually fails.
		if err := enc.Close(); !errs.HasCode(err, CodeXMLMarshalFailed) {
			t.Errorf("%s: Close err=%v, want CodeXMLMarshalFailed (0.3.3.1)", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
