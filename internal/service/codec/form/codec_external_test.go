package form_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/form"
)

// TestNew verifies the constructor returns a non-nil singleton with the
// canonical name.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"canonical name", "form"}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := form.New()
		if c == nil {
			t.Fatalf("%s: New returned nil", tc.name)
		}
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

// TestAliasTablesAreDefensiveCopies pins the SDK-wide convention that a codec
// hands out clones of its MIME / extension tables, so a caller mutating the
// returned slice cannot poison the registry for the whole process.
func TestAliasTablesAreDefensiveCopies(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		read func() []string
	}
	tests := []tc{
		{"MIMETypes", form.New().MIMETypes},
		{"Extensions", form.New().Extensions},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		first := tc.read()
		if len(first) == 0 {
			t.Fatalf("%s: empty alias table", tc.name)
		}
		//: mutate the copy the codec handed over.
		first[0] = "poisoned"
		//: a second read must be unaffected.
		if second := tc.read(); second[0] == "poisoned" {
			t.Errorf("%s: alias table is shared, not cloned", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal covers every accepted input shape plus the VALUE_INVALID
// rejection, and pins the canonical (key-sorted) output.
func TestMarshal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		want    string
		wantErr string
	}
	tests := []tc{
		{"url.Values sorted by key", url.Values{"z": {"26"}, "a": {"1"}}, "a=1&z=26", ""},
		{"repeated key keeps wire order", url.Values{"a": {"1", "2"}}, "a=1&a=2", ""},
		{"map[string][]string", map[string][]string{"a": {"1", "2"}}, "a=1&a=2", ""},
		{"map[string]string widened", map[string]string{"a": "1"}, "a=1", ""},
		{"pointer form", &url.Values{"a": {"1"}}, "a=1", ""},
		{"escaping", url.Values{"a b": {"c&d"}}, "a+b=c%26d", ""},
		{"empty map encodes to empty body", url.Values{}, "", ""},
		{"key bound to empty slice vanishes", url.Values{"a": {}, "b": {"2"}}, "b=2", ""},
		{"non-form input", 42, "", "VALUE_INVALID"},
		{"map[string]any is not a form shape", map[string]any{"a": 1}, "", "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		data, err := form.New().Marshal(tc.in)
		if tc.wantErr == "" {
			if err != nil {
				t.Fatalf("%s: Marshal err=%v", tc.name, err)
			}
			if got := string(data); got != tc.want {
				t.Errorf("%s: Marshal=%q want %q", tc.name, got, tc.want)
			}
			return
		}
		if !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshalMultiValueTarget covers the full-fidelity target shapes plus
// every decode refusal.
func TestUnmarshalMultiValueTarget(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		data    string
		want    url.Values
		wantErr string
	}
	tests := []tc{
		{"single pair", "a=1", url.Values{"a": {"1"}}, ""},
		{"repeated key collects both values in wire order", "a=2&a=1", url.Values{"a": {"2", "1"}}, ""},
		{"plus decodes to space", "a=b+c", url.Values{"a": {"b c"}}, ""},
		{"percent escape folds", "a=%26", url.Values{"a": {"&"}}, ""},
		{"bare key yields empty value", "a", url.Values{"a": {""}}, ""},
		{"empty body yields empty values", "", url.Values{}, ""},
		{"bad percent escape", "a=%zz", nil, "UNMARSHAL_FAILED"},
		{"semicolon separator is refused", "a=1;b=2", nil, "UNMARSHAL_FAILED"},
		{"oversize body", strings.Repeat("a", 8*1024*1024+1), nil, "UNMARSHAL_FAILED"},
		{"too many pairs", strings.Repeat("a=1&", 10_000) + "a=1", nil, "UNMARSHAL_FAILED"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		var got url.Values
		err := form.New().Unmarshal([]byte(tc.data), &got)
		if tc.wantErr != "" {
			if !errs.HasReason(err, tc.wantErr) {
				t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, err)
		}
		if gotEnc, wantEnc := got.Encode(), tc.want.Encode(); gotEnc != wantEnc {
			t.Errorf("%s: values=%q want %q", tc.name, gotEnc, wantEnc)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestUnmarshalTargetShapes covers the three accepted pointer targets, the
// MULTI_VALUE refusal that keeps the single-valued target honest, and the
// VALUE_INVALID rejections.
func TestUnmarshalTargetShapes(t *testing.T) {
	t.Parallel()
	//: targetKind lets each subtest allocate its own destination so
	//: parallel runs never share one.
	type targetKind int
	const (
		kindValues targetKind = iota
		kindMulti
		kindSingle
		kindNilValues
		kindNilSingle
		kindString
	)
	type tc struct {
		name    string
		data    string
		kind    targetKind
		wantErr string
	}
	tests := []tc{
		{"*url.Values", "a=1&a=2", kindValues, ""},
		{"*map[string][]string", "a=1&a=2", kindMulti, ""},
		{"*map[string]string single-valued body", "a=1&b=2", kindSingle, ""},
		{"*map[string]string refuses a repeated key", "a=1&a=2", kindSingle, "MULTI_VALUE"},
		{"nil *url.Values", "a=1", kindNilValues, "VALUE_INVALID"},
		{"nil *map[string]string", "a=1", kindNilSingle, "VALUE_INVALID"},
		{"non-form target", "a=1", kindString, "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		//: allocate a per-subtest target so parallel runs don't race.
		var target any
		switch tc.kind {
		case kindValues:
			target = &url.Values{}
		case kindMulti:
			target = &map[string][]string{}
		case kindSingle:
			target = &map[string]string{}
		case kindNilValues:
			var nilValues *url.Values
			target = nilValues
		case kindNilSingle:
			var nilSingle *map[string]string
			target = nilSingle
		case kindString:
			s := ""
			target = &s
		}
		err := form.New().Unmarshal([]byte(tc.data), target)
		if tc.wantErr == "" && err != nil {
			t.Errorf("%s: Unmarshal err=%v", tc.name, err)
		}
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRoundTripContract pins the exact round-trip promise the package
// CLAUDE.md makes — including the two places where it is value-level and NOT
// byte-level, which are asserted rather than glossed over.
func TestRoundTripContract(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: wire bytes fed to Unmarshal.
		data string
		//: bytes Marshal emits for the decoded value; differs from data
		//: exactly when the input was not already in canonical form.
		wantReencoded string
	}
	tests := []tc{
		{"canonical input is a byte-level fixpoint", "a=1&a=2&b=3", "a=1&a=2&b=3"},
		{"unsorted input is canonicalised, not preserved", "b=2&a=1", "a=1&b=2"},
		{"bare key gains its '='", "a", "a="},
		{"space round-trips through '+'", "a=b+c", "a=b+c"},
		{"an encoded plus stays a plus, not a space", "a=b%2Bc", "a=b%2Bc"},
		{"lowercase escape is re-emitted uppercase", "a=%2f", "a=%2F"},
		{"trailing separator is dropped", "a=1&", "a=1"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		c := form.New()
		var decoded url.Values
		if err := c.Unmarshal([]byte(tc.data), &decoded); err != nil {
			t.Fatalf("%s: Unmarshal err=%v", tc.name, err)
		}
		reencoded, err := c.Marshal(decoded)
		if err != nil {
			t.Fatalf("%s: Marshal err=%v", tc.name, err)
		}
		canonical := string(reencoded)
		if canonical != tc.wantReencoded {
			t.Fatalf("%s: re-encoded %q want %q", tc.name, canonical, tc.wantReencoded)
		}
		//: whatever the byte-level answer, the SECOND round-trip is always
		//: a fixpoint: canonical bytes in, identical bytes out.
		var again url.Values
		if err := c.Unmarshal(reencoded, &again); err != nil {
			t.Fatalf("%s: second Unmarshal err=%v", tc.name, err)
		}
		twice, err := c.Marshal(again)
		if err != nil {
			t.Fatalf("%s: second Marshal err=%v", tc.name, err)
		}
		if string(twice) != canonical {
			t.Errorf("%s: not idempotent: %q then %q", tc.name, canonical, twice)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestAppend covers the optional Appender extension: the caller's prefix must
// survive, and a rejected value must leave dst byte-identical.
func TestAppend(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      any
		want    string
		wantErr string
	}
	tests := []tc{
		{"appends after the prefix", url.Values{"a": {"1"}}, "PREFIX" + "a=1", ""},
		{"empty value appends nothing", url.Values{}, "PREFIX", ""},
		{"rejection leaves dst untouched", 42, "PREFIX", "VALUE_INVALID"},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		appender, ok := form.New().(codec.Appender)
		if !ok {
			t.Fatalf("%s: form codec does not implement codec.Appender", tc.name)
		}
		got, err := appender.Append([]byte("PREFIX"), tc.in)
		if tc.wantErr != "" && !errs.HasReason(err, tc.wantErr) {
			t.Errorf("%s: expected %s, got %v", tc.name, tc.wantErr, err)
		}
		if tc.wantErr == "" && err != nil {
			t.Fatalf("%s: Append err=%v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: Append=%q want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestRegisteredViaImport verifies the codec self-registers on package load.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		check func() bool
	}
	tests := []tc{
		{"format registered", func() bool { _, ok := codec.Lookup(codec.Format("form")); return ok }},
		{"MIME resolved", func() bool {
			_, ok := codec.LookupMIME("application/x-www-form-urlencoded")
			return ok
		}},
		{"extension resolved", func() bool { _, ok := codec.LookupExt(".form"); return ok }},
		{"alias extension resolved", func() bool { _, ok := codec.LookupExt(".urlencoded"); return ok }},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		if !tc.check() {
			t.Errorf("%s: lookup failed", tc.name)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
