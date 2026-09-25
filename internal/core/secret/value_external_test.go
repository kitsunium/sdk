package secret_test

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/secret"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// plainSecret is the fixture every rendering test hunts for. It is long and
// distinctive enough that a substring match cannot happen by chance.
const plainSecret string = "hunter2-correct-horse-battery"

// forbiddenSpellings lists every form in which plainSecret could leak: the
// text itself, the byte list fmt prints for a []byte field, and the hex and
// base64 encodings a careless rendering would reach for.
func forbiddenSpellings() []string {
	//: the byte list fmt prints for a []byte field: "104 117 110 ...".
	decimal := make([]string, 0, len(plainSecret))
	for _, b := range []byte(plainSecret) {
		decimal = append(decimal, strconv.Itoa(int(b)))
	}
	return []string{
		plainSecret,
		strings.Join(decimal, " "),
		hex.EncodeToString([]byte(plainSecret)),
		base64.StdEncoding.EncodeToString([]byte(plainSecret)),
	}
}

// holder carries a Value in an EXPORTED field.
type holder struct {
	Password secret.Value
}

// hider carries a Value in an UNEXPORTED field, which fmt formats by
// reflection instead of through the Value's methods.
type hider struct {
	password secret.Value
}

// TestValueNeverRendersItsSecret is the domain's central claim, asserted over
// every rendering a Go program reaches for by accident.
func TestValueNeverRendersItsSecret(t *testing.T) {
	t.Parallel()
	value := secret.FromString(plainSecret)
	type tc struct {
		name   string
		render func() string
		// opaque is set where fmt cannot reach the Value's methods — an
		// unexported field — and prints the struct by reflection instead.
		// There the claim is weaker and exact: no spelling of the secret, and
		// no placeholder either, because nothing called Format.
		opaque bool
	}
	marshal := func(v any) string {
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
		return string(encoded)
	}
	tests := []tc{
		{"%v", func() string { return fmt.Sprintf("%v", value) }, false},
		{"%+v", func() string { return fmt.Sprintf("%+v", value) }, false},
		{"%#v", func() string { return fmt.Sprintf("%#v", value) }, false},
		{"%s", func() string { return fmt.Sprintf("%s", value) }, false},
		{"%q", func() string { return fmt.Sprintf("%q", value) }, false},
		{"%x", func() string { return fmt.Sprintf("%x", value) }, false},
		{"%X", func() string { return fmt.Sprintf("%X", value) }, false},
		{"%d", func() string { return fmt.Sprintf("%d", value) }, false},
		{"a pointer", func() string { return fmt.Sprintf("%+v", &value) }, false},
		{"String", value.String, false},
		{"GoString", value.GoString, false},
		{"an exported field, %+v", func() string { return fmt.Sprintf("%+v", holder{Password: value}) }, false},
		{"an exported field, %#v", func() string { return fmt.Sprintf("%#v", holder{Password: value}) }, false},
		{"an unexported field, %+v", func() string { return fmt.Sprintf("%+v", hider{password: value}) }, true},
		{"an unexported field, %#v", func() string { return fmt.Sprintf("%#v", &hider{password: value}) }, true},
		{"a slice", func() string { return fmt.Sprintf("%v", []secret.Value{value}) }, false},
		{"a map", func() string { return fmt.Sprintf("%v", map[string]secret.Value{"k": value}) }, false},
		{"json", func() string { return marshal(value) }, false},
		{"json of a struct", func() string { return marshal(holder{Password: value}) }, false},
		{"json of a pointer", func() string { return marshal(&value) }, false},
		{"json of a map", func() string { return marshal(map[string]secret.Value{"k": value}) }, false},
		{"MarshalText", func() string {
			text, err := value.MarshalText()
			if err != nil {
				t.Fatalf("MarshalText: %v", err)
			}
			return string(text)
		}, false},
		{"a VersionValue", func() string {
			return fmt.Sprintf("%+v %s", secret.VersionValue{Name: "db", Version: 3, Value: value},
				marshal(secret.VersionValue{Name: "db", Version: 3, Value: value}))
		}, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		rendered := c.render()
		//: through the methods, the placeholder is what was written.
		if !c.opaque && !strings.Contains(rendered, "redacted") {
			t.Errorf("%s rendered %q, which does not carry the placeholder", c.name, rendered)
		}
		//: and no spelling of the secret may appear.
		for _, forbidden := range forbiddenSpellings() {
			if strings.Contains(rendered, forbidden) {
				t.Fatalf("%s rendered the secret (%q) in %q", c.name, forbidden, rendered)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValueRevealIsTheOnlyWayOut pins the two exits and their copying: the
// caller's buffer is not the secret's storage, and neither is what Reveal
// hands back.
func TestValueRevealIsTheOnlyWayOut(t *testing.T) {
	t.Parallel()
	raw := []byte(plainSecret)
	value := secret.NewValue(raw)
	//: the caller clears their buffer, as anyone handling a secret should.
	clear(raw)
	if value.RevealString() != plainSecret {
		t.Fatalf("clearing the constructor's input changed the secret to %q", value.RevealString())
	}
	revealed := value.Reveal()
	clear(revealed)
	if value.RevealString() != plainSecret {
		t.Fatalf("clearing a revealed copy changed the secret to %q", value.RevealString())
	}
	if value.Len() != len(plainSecret) {
		t.Errorf("Len() = %d, want %d", value.Len(), len(plainSecret))
	}
	if value.IsZero() {
		t.Error("a Value holding a secret reports IsZero")
	}
}

// TestZeroValueIsTheEmptySecret pins what an unfilled field holds.
func TestZeroValueIsTheEmptySecret(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		value secret.Value
	}
	tests := []tc{
		{"the zero Value", secret.Value{}},
		{"NewValue(nil)", secret.NewValue(nil)},
		{"NewValue(empty)", secret.NewValue([]byte{})},
		{"FromString(empty)", secret.FromString("")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.value.IsZero() || c.value.Len() != 0 {
			t.Errorf("%s: IsZero=%v Len=%d, want true 0", c.name, c.value.IsZero(), c.value.Len())
		}
		if c.value.Reveal() != nil || c.value.RevealString() != "" {
			t.Errorf("%s revealed something", c.name)
		}
		//: the zero Value renders the placeholder too: a rendering that
		//: varied with the content would say whether a secret is set.
		if fmt.Sprint(c.value) != secret.Redacted {
			t.Errorf("%s rendered %q", c.name, fmt.Sprint(c.value))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValueEqual pins the comparison every caller must use, since == does not
// compile.
func TestValueEqual(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		left, right secret.Value
		want        bool
	}
	tests := []tc{
		{"the same bytes, built twice", secret.FromString("abc"), secret.NewValue([]byte("abc")), true},
		{"different bytes", secret.FromString("abc"), secret.FromString("abd"), false},
		{"a prefix", secret.FromString("abc"), secret.FromString("ab"), false},
		{"a secret and the zero Value", secret.FromString("abc"), secret.Value{}, false},
		{"two zero Values", secret.Value{}, secret.FromString(""), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.left.Equal(c.right); got != c.want {
			t.Errorf("%s: Equal = %v, want %v", c.name, got, c.want)
		}
		if got := c.right.Equal(c.left); got != c.want {
			t.Errorf("%s: Equal is not symmetric", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestValueDecodes pins the inbound half: a Value field fills from a JSON
// string or from text exactly as a string field would.
func TestValueDecodes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		json string
		want string
	}
	tests := []tc{
		{"a plain string", `{"Password":"hunter2"}`, "hunter2"},
		{"escapes are resolved", `{"Password":"a\"b\\cé"}`, "a\"b\\cé"},
		{"a digit string stays digits", `{"Password":"12345678901234567890123"}`, "12345678901234567890123"},
		{"an empty string is the empty secret", `{"Password":""}`, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got holder
		if err := json.Unmarshal([]byte(c.json), &got); err != nil {
			t.Fatalf("%s: Unmarshal: %v", c.name, err)
		}
		if got.Password.RevealString() != c.want {
			t.Errorf("%s: decoded %q, want %q", c.name, got.Password.RevealString(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: an explicit null is "not supplied" and leaves the field as it was.
	kept := holder{Password: secret.FromString("kept")}
	if err := json.Unmarshal([]byte(`{"Password":null}`), &kept); err != nil {
		t.Fatalf("null: %v", err)
	}
	if kept.Password.RevealString() != "kept" {
		t.Errorf("null replaced the secret with %q", kept.Password.RevealString())
	}
	//: text is the other inbound path.
	var fromText secret.Value
	if err := fromText.UnmarshalText([]byte("from-text")); err != nil || fromText.RevealString() != "from-text" {
		t.Errorf("UnmarshalText = (%q, %v)", fromText.RevealString(), err)
	}
}

// TestValueRefusesWhatWouldBeRespelled pins the two refusals and that neither
// quotes its input.
func TestValueRefusesWhatWouldBeRespelled(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		json string
	}
	tests := []tc{
		{"an integer", `{"Password":12345}`},
		{"a large integer", `{"Password":12345678901234567890123}`},
		{"a float", `{"Password":1e3}`},
		{"a boolean", `{"Password":true}`},
		{"an object", `{"Password":{"a":"b"}}`},
		{"an array", `{"Password":["a"]}`},
		{"the placeholder", `{"Password":"<redacted>"}`},
		{"the placeholder as encoding/json writes it", `{"Password":"<redacted>"}`},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var got holder
		err := json.Unmarshal([]byte(c.json), &got)
		if !errs.HasCode(err, secret.CodeValueRefused) {
			t.Fatalf("%s: Unmarshal = %v, want CodeValueRefused", c.name, err)
		}
		if !got.Password.IsZero() {
			t.Errorf("%s: a refused decode left a secret behind", c.name)
		}
		//: the refusal names nothing it was given.
		for _, fragment := range []string{"12345", "1e3", "true", `"a"`} {
			if strings.Contains(c.json, fragment) && strings.Contains(err.Error(), fragment) {
				t.Errorf("%s: the refusal %q quotes its input", c.name, err.Error())
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the text path refuses the placeholder too.
	var fromText secret.Value
	if err := fromText.UnmarshalText([]byte(secret.Redacted)); !errs.HasCode(err, secret.CodeValueRefused) {
		t.Errorf("UnmarshalText(placeholder) = %v, want CodeValueRefused", err)
	}
}

// TestADumpedConfigurationCannotBeReloaded is the failure the placeholder
// refusal exists for: a configuration rendered with json.Marshal and read back
// must fail loudly, not load with every secret replaced by the placeholder.
func TestADumpedConfigurationCannotBeReloaded(t *testing.T) {
	t.Parallel()
	dumped, err := json.Marshal(holder{Password: secret.FromString(plainSecret)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var reloaded holder
	if err := json.Unmarshal(dumped, &reloaded); !errs.HasCode(err, secret.CodeValueRefused) {
		t.Fatalf("reloading a dump = %v (secret %q), want CodeValueRefused", err, reloaded.Password.RevealString())
	}
}
