package redact_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/redact"
)

// frameworkRedactor is configured the way a framework with its own tag and
// its own binding tags configures it: `kit:"secret"`, and a field bound to a
// cookie or to a header named like a secret is secret too.
func frameworkRedactor() *redact.Redactor {
	var r *redact.Redactor
	r = redact.NewRedactor(redact.Config{
		Tag: "kit",
		Field: func(field reflect.StructField) bool {
			if _, cookie := field.Tag.Lookup("cookie"); cookie {
				return true
			}
			if header, bound := field.Tag.Lookup("header"); bound {
				name, _, _ := strings.Cut(header, ",")
				return r.Name(name)
			}
			return false
		},
	})
	return r
}

// TestValueReplacesEverySecretItRecognises is the framework's own acceptance
// test, carried over: secrets by name, by declaration through a pointer, a
// slice, a map and an embedded struct, and by the caller's extra field rule —
// and nothing that is not a secret.
func TestValueReplacesEverySecretItRecognises(t *testing.T) {
	t.Parallel()
	type inner struct {
		Name   string `json:"name"`
		Recipe string `json:"recipe" kit:"secret"`
	}
	type Embedded struct {
		Pin string `json:"pin" kit:"other,secret"`
	}
	type outer struct {
		Embedded
		User     string            `json:"user"`
		Password string            `json:"password"`
		Items    []inner           `json:"items"`
		ByKey    map[string]inner  `json:"byKey"`
		Next     *outer            `json:"next,omitempty"`
		Cookie   string            `json:"Cookie" cookie:"sid"`
		Auth     string            `json:"Auth" header:"X-Api-Key"`
		Accept   string            `json:"Accept" header:"Accept"`
		Notes    map[string]string `json:"notes"`
		When     time.Time         `json:"when"`
	}
	v := outer{
		Pin: "1234", User: "ann", Password: "p", Items: []inner{{Name: "a", Recipe: "r1"}},
		ByKey: map[string]inner{"k": {Name: "b", Recipe: "r2"}}, Next: &outer{User: "bob", Items: []inner{{Recipe: "r3"}}},
		Cookie: "c", Auth: "key-9", Accept: "json", Notes: map[string]string{"refresh_token": "t", "ok": "fine"},
	}
	redacted, err := frameworkRedactor().Value(v, 8<<10)
	if err != nil || redacted.Truncated {
		t.Fatalf("Value() = %v (truncated %v), want a whole copy", err, redacted.Truncated)
	}
	text := string(redacted.JSON)
	for _, leaked := range []string{"1234", `"p"`, "r1", "r2", "r3", `"c"`, "key-9", `"t"`} {
		if strings.Contains(text, leaked) {
			t.Errorf("%s leaked: %s", leaked, text)
		}
	}
	for _, kept := range []string{`"user":"ann"`, `"name":"a"`, `"user":"bob"`, `"Accept":"json"`, `"ok":"fine"`} {
		if !strings.Contains(text, kept) {
			t.Errorf("%s is not a secret but was not kept: %s", kept, text)
		}
	}
	if v.Password != "p" || v.Items[0].Recipe != "r1" || v.Notes["refresh_token"] != "t" {
		t.Error("Value modified the value it was given")
	}
}

// TestJSONJudgesADocumentByItsNames pins the redaction of a document no Go
// type describes: names alone, at any depth, whatever the value — an object
// under a secret's name is replaced whole — and URL credentials in every
// string.
func TestJSONJudgesADocumentByItsNames(t *testing.T) {
	t.Parallel()
	document := []byte(`{"Authorization":"Bearer x","nested":[{"apiKey":1,"session":{"id":2}}],"url":"https://u:pw@h/x"}`)
	original := bytes.Clone(document)
	redacted, err := redact.NewRedactor(redact.Config{}).JSON(document, 1024)
	if err != nil {
		t.Fatalf("JSON() = %v", err)
	}
	want := `{"Authorization":"[redacted]","nested":[{"apiKey":"[redacted]","session":"[redacted]"}],"url":"https://[redacted]@h/x"}`
	if string(redacted.JSON) != want {
		t.Errorf("JSON() =\n  %s\nwant\n  %s", redacted.JSON, want)
	}
	if !bytes.Equal(document, original) {
		t.Error("JSON modified the document it was given")
	}
}

// TestTheDefaultTagIsRedact pins the SDK's own spelling of a declared secret,
// and that a Redactor with no words still recognises the default ones — a
// redactor that recognised no name would be a formatter.
func TestTheDefaultTagIsRedact(t *testing.T) {
	t.Parallel()
	type account struct {
		Email    string `json:"email"`
		Recovery string `json:"recovery" redact:"secret"`
		Token    string `json:"token"`
	}
	for _, words := range [][]string{nil, {}} {
		redacted, err := redact.NewRedactor(redact.Config{Words: words}).Value(account{Email: "a@b", Recovery: "xyz-9", Token: "value-7"}, 1024)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(redacted.JSON), "xyz-9") || strings.Contains(string(redacted.JSON), "value-7") {
			t.Errorf("Words %#v: %s", words, redacted.JSON)
		}
	}
	custom := redact.NewRedactor(redact.Config{Words: []string{"email"}})
	if !custom.Name("Primary-Email") || custom.Name("password") {
		t.Error("custom Words must replace the defaults, case-insensitively")
	}
	if !redact.NewRedactor(redact.Config{}).Name("X-Api-Key") {
		t.Error("the default words must recognise X-Api-Key")
	}
}

// TestTheBoundIsExactAndTheOutputAlwaysWellFormed sweeps every bound from the
// floor to past the document's own length over a document holding every
// shape a cut can land in — nested containers, escapes, a multi-byte string,
// a long number — and asserts the two promises at every one: never longer
// than the bound, always one well-formed JSON value.
func TestTheBoundIsExactAndTheOutputAlwaysWellFormed(t *testing.T) {
	t.Parallel()
	document := []byte(`{"a":[1,2,{"b":"tab\there \"quoted\" \\ é漢字"},[],{}],"c":12345678901234567890,` +
		`"d":{"e":{"f":[true,false,null]}},"g":"` + strings.Repeat("x", 80) + `","password":"p"}`)
	r := redact.NewRedactor(redact.Config{})
	whole, err := r.JSON(document, len(document)*2)
	if err != nil || whole.Truncated {
		t.Fatalf("JSON() of the whole = %v, truncated %v", err, whole.Truncated)
	}
	for limit := redact.MinBytes; limit <= len(whole.JSON)+4; limit++ {
		redacted, err := r.JSON(document, limit)
		if err != nil {
			t.Fatalf("bound %d: %v", limit, err)
		}
		if len(redacted.JSON) > limit {
			t.Fatalf("bound %d: %d bytes: %s", limit, len(redacted.JSON), redacted.JSON)
		}
		if !json.Valid(redacted.JSON) {
			t.Fatalf("bound %d: not JSON: %s", limit, redacted.JSON)
		}
		if redacted.Truncated != (limit < len(whole.JSON)) {
			t.Fatalf("bound %d: truncated %v for a %d-byte whole", limit, redacted.Truncated, len(whole.JSON))
		}
	}
	if below, err := r.JSON(document, 1); err != nil || len(below.JSON) > redact.MinBytes {
		t.Errorf("a bound below the floor = %d bytes, %v; want at most MinBytes", len(below.JSON), err)
	}
}

// TestJSONRefusesWhatIsNotOneDocument pins the refusals, and that neither
// repeats the document or the value.
func TestJSONRefusesWhatIsNotOneDocument(t *testing.T) {
	t.Parallel()
	r := redact.NewRedactor(redact.Config{})
	for _, document := range []string{`{"password":"hunter2"`, `{"a":1} {"b":2}`, `hunter2`, ``} {
		_, err := r.JSON([]byte(document), 1024)
		if !errs.HasCode(err, redact.CodeDocumentInvalid) {
			t.Errorf("JSON(%q) = %v, want DOCUMENT_INVALID", document, err)
		}
		if strings.Contains(err.Error(), "hunter2") || strings.Contains(errs.PrivateOf(err), "hunter2") {
			t.Errorf("the refusal repeated the document: %v", err)
		}
	}
	type withChannel struct {
		Secret string
		Stream chan int
	}
	_, err := r.Value(withChannel{Secret: "hunter2"}, 1024)
	if !errs.HasCode(err, redact.CodeValueUnencodable) || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("Value(unencodable) = %v, want VALUE_UNENCODABLE naming no value", err)
	}
	for _, field := range errs.FieldsOf(err) {
		if strings.Contains(field.StringValue(), "hunter2") {
			t.Errorf("field %s repeated the value", field.Key())
		}
	}
	if errors.Unwrap(err) != nil && strings.Contains(errors.Unwrap(err).Error(), "hunter2") {
		t.Error("the chain repeated the value")
	}
}

// TestTextScrubsBeforeItCuts pins the order that makes the cut safe: a cut
// applied first can land between a password and the "@" that marks it, and
// the password is then shown. Scrubbing first means the cut only ever
// shortens what is already safe.
func TestTextScrubsBeforeItCuts(t *testing.T) {
	t.Parallel()
	r := redact.NewRedactor(redact.Config{})
	type tc struct {
		name     string
		text     string
		maxBytes int
		want     string
	}
	tests := []tc{
		{"credentials in a URL", "dial postgres://app:hunter2@db:5432/x failed", 1024, "dial postgres://[redacted]@db:5432/x failed"},
		{"an @ inside the password", "https://user:p@ss@host/x", 1024, "https://[redacted]@host/x"},
		{"a URL without credentials", "see https://example.com/a@b", 1024, "see https://example.com/a@b"},
		{"two URLs", "https://a:b@h1 and https://c@h2", 1024, "https://[redacted]@h1 and https://[redacted]@h2"},
		{"a cut inside the credentials", "connecting to https://admin:hunter2@db", 30, "connecting to https://[reda…"},
		{"a cut at a rune boundary", "héhéhéhéhéhéhéhé", 16, "héhéhéhéh…"},
		{"a bound below the floor", strings.Repeat("x", 40), 1, strings.Repeat("x", 13) + "…"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := r.Text(c.text, c.maxBytes)
		if got != c.want {
			t.Errorf("Text() = %q, want %q", got, c.want)
		}
		if strings.Contains(got, "hunter2") || strings.Contains(got, "hunter") {
			t.Errorf("Text() showed the password: %q", got)
		}
		if len(got) > max(c.maxBytes, redact.MinBytes) {
			t.Errorf("Text() = %d bytes over a bound of %d", len(got), c.maxBytes)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
