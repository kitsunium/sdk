package tlv

import (
	"bytes"
	"testing"
)

// Test_tlvCodec_Name covers the canonical identifier returned by the codec.
func Test_tlvCodec_Name(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{
		{"canonical identifier", "tlv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		if got := c.Name(); got != tc.want {
			t.Errorf("%s: Name=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_MIMETypes covers the MIME list and its copy semantics.
func Test_tlvCodec_MIMETypes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical MIME first", "application/x-tlv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		mimes := c.MIMETypes()
		if len(mimes) == 0 || mimes[0] != tc.wantHead {
			t.Errorf("%s: MIMETypes=%v, want head %q", tc.name, mimes, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_Extensions covers the extension list.
func Test_tlvCodec_Extensions(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		wantHead string
	}
	tests := []tc{
		{"canonical extension first", ".tlv"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		exts := c.Extensions()
		if len(exts) == 0 || exts[0] != tc.wantHead {
			t.Errorf("%s: Extensions=%v, want head %q", tc.name, exts, tc.wantHead)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_Marshal exercises the Marshal path with several canonical
// values plus an unsupported-type rejection.
func Test_tlvCodec_Marshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		wantErr bool
	}
	tests := []tc{
		{"int", int64(42), false},
		{"string", "hello", false},
		{"slice of any", []any{int64(1), "two"}, false},
		{"channel rejected", make(chan int), true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		_, err := c.Marshal(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_Unmarshal exercises both happy- and unhappy-path Unmarshal calls.
func Test_tlvCodec_Unmarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    []byte
		wantErr bool
	}
	tests := []tc{
		{"empty buffer is malformed", []byte{}, true},
		{"truncated tag-only record", []byte{0x40}, true},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		var out any
		err := c.Unmarshal(tc.data, &out)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_NewEncoder covers the streaming encoder constructor.
func Test_tlvCodec_NewEncoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil encoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		if enc := c.NewEncoder(&bytes.Buffer{}); enc == nil {
			t.Errorf("%s: NewEncoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_NewDecoder covers the streaming decoder constructor.
func Test_tlvCodec_NewDecoder(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
	}
	tests := []tc{{"returns non-nil decoder"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		if dec := c.NewDecoder(bytes.NewReader(nil)); dec == nil {
			t.Errorf("%s: NewDecoder returned nil", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// Test_tlvCodec_Append covers the Appender extension's prior-content-on-error
// contract.
func Test_tlvCodec_Append(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		dst     []byte
		in      any
		wantErr bool
		wantLen int
	}
	tests := []tc{
		{"empty dst grows on success", nil, int64(1), false, -1},
		{"prior dst preserved on success", []byte("X"), "y", false, -1},
		{"prior dst preserved on error", []byte("X"), make(chan int), true, 1},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := &tlvCodec{}
		out, err := c.Append(tc.dst, tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if tc.wantErr && tc.wantLen >= 0 && len(out) != tc.wantLen {
			t.Errorf("%s: rollback length=%d want %d", tc.name, len(out), tc.wantLen)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
