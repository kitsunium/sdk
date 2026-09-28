package secret_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestSubjectKeyStoreIsFrozenAtFiveMethods pins the published port's method
// set (ADR 0039): a sixth method would break every store a caller wrote, so a
// new capability is a sibling interface, and this test makes adding one here a
// decision rather than an edit.
func TestSubjectKeyStoreIsFrozenAtFiveMethods(t *testing.T) {
	t.Parallel()
	storeType := reflect.TypeFor[secret.SubjectKeyStore]()
	got := make([]string, 0, storeType.NumMethod())
	for method := range storeType.Methods() {
		got = append(got, method.Name)
	}
	want := []string{"All", "Delete", "Get", "Insert", "Replace"}
	//: reflect lists methods in lexical order, so the comparison is exact.
	if !slices.Equal(got, want) {
		t.Fatalf("SubjectKeyStore methods = %v, want %v — widen by a sibling interface, never here", got, want)
	}
}

// TestValidateSubjectAcceptsDerivedReferences pins the accepted side: the
// encodings a caller derives a reference in — hexadecimal digests, prefixed
// namespaces, lowercase UUIDs — and both ends of the length range.
func TestValidateSubjectAcceptsDerivedReferences(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
	}
	tests := []tc{
		{"one letter", "a"},
		{"one digit", "7"},
		{"an HMAC-SHA256 in hex", strings.Repeat("9f", 32)},
		{"a SHA-512 in hex, the longest subject", strings.Repeat("a", secret.MaxSubjectLen)},
		{"a namespaced reference", "user:4f2a"},
		{"a record's own key", "rec:reports.v1:0192f7aa"},
		{"a lowercase UUID", "0192f7aa-5b1c-7d3e-9f00-1a2b3c4d5e6f"},
		{"an underscore inside", "tenant_42"},
		{"a trailing separator", "a-"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if err := secret.ValidateSubject(c.input); err != nil {
			t.Errorf("%s: ValidateSubject(%q) = %v, want nil", c.name, c.input, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValidateSubjectRefusesEverythingElse pins the refused side — above all
// the uppercase letter, which a case-insensitive collation would fold into
// another subject's key — and that a refusal never repeats what it refused:
// the string most likely to be refused is an identity.
func TestValidateSubjectRefusesEverythingElse(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
	}
	tests := []tc{
		{"empty", ""},
		{"one past the bound", strings.Repeat("a", secret.MaxSubjectLen+1)},
		{"an uppercase letter, folded by a case-insensitive store", "User:4F2A"},
		{"an e-mail address, the identity itself", "jane.doe@example.org"},
		{"a leading dot", ".hidden"},
		{"a leading dash", "-x"},
		{"a leading colon", ":x"},
		{"a parent reference", ".."},
		{"a slash", "a/b"},
		{"a space", "jane doe"},
		{"a NUL", "a\x00b"},
		{"a newline", "a\nb"},
		{"a non-ASCII letter", "clé"},
		{"base64 padding", "YWJj="},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := secret.ValidateSubject(c.input)
		if !errs.HasCode(err, secret.CodeInvalidSubject) {
			t.Fatalf("%s: ValidateSubject(%q) = %v, want CodeInvalidSubject", c.name, c.input, err)
		}
		//: the refused string never travels, in any field.
		if c.input != "" && strings.Contains(errs.PrivateOf(err)+err.Error()+fieldText(err), c.input) {
			t.Errorf("%s: the refusal repeats the refused string", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
