package secret_test

import (
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// TestValidateNameAcceptsTheLabelGrammar pins the accepted side of the closed
// alphabet, including both ends of the length range.
func TestValidateNameAcceptsTheLabelGrammar(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
	}
	tests := []tc{
		{"one letter", "a"},
		{"one digit", "7"},
		{"a hyphenated name", "smtp-url"},
		{"consecutive hyphens inside", "a--b"},
		{"digits and letters", "k8s-signing-key-2"},
		{"the longest name", strings.Repeat("a", secret.MaxNameLen)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if err := secret.ValidateName(c.input); err != nil {
			t.Errorf("%s: ValidateName(%q) = %v, want nil", c.name, c.input, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValidateNameRefusesEverythingElse pins the refused side, and that a
// refusal never repeats the string it refused — the classic invalid name is a
// value passed where a name belonged.
func TestValidateNameRefusesEverythingElse(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		input string
	}
	tests := []tc{
		{"empty", ""},
		{"one past the bound", strings.Repeat("a", secret.MaxNameLen+1)},
		{"an uppercase letter", "Smtp-url"},
		{"an underscore, which would collide with '-' in the environment", "smtp_url"},
		{"a dot", "smtp.url"},
		{"a slash", "a/b"},
		{"a backslash", `a\b`},
		{"a parent reference", ".."},
		{"a leading hyphen", "-smtp"},
		{"a trailing hyphen", "smtp-"},
		{"a space", "smtp url"},
		{"a NUL", "smtp\x00url"},
		{"a non-ASCII letter", "clé"},
		{"a value passed as a name", "hunter2 correct horse"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := secret.ValidateName(c.input)
		if !errs.HasCode(err, secret.CodeInvalidName) {
			t.Fatalf("%s: ValidateName(%q) = %v, want CodeInvalidName", c.name, c.input, err)
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

// fieldText flattens every field an error carries, so a test can assert what
// a log line built from it would contain.
func fieldText(err error) string {
	var builder strings.Builder
	for _, field := range errs.FieldsOf(err) {
		builder.WriteString(field.Key())
		builder.WriteString("=")
		builder.WriteString(field.StringValue())
		builder.WriteString(" ")
	}
	return builder.String()
}
