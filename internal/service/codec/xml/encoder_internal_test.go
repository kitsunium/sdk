package xml

import (
	"bytes"
	stdxml "encoding/xml"
	"testing"
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
