// Package otel_test — the shared OpenTelemetry model: typed attributes,
// Resource and InstrumentationScope, and the refusal each one borrows from the
// signal that calls it.
package otel_test

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/observe/otel"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// testDefaultScope is the default a test signal hands NormalizeScope.
const testDefaultScope string = "github.com/acme/signal"

// The two refusals a test hands the model, standing in for two signals. They
// carry different codes on purpose: what is pinned is that the panic carries
// the CALLER'S code, and one sentinel could not tell that apart from a code the
// package had picked for itself.
var (
	firstRefusal = errs.Define(errs.Pack(0, 2, 0xFE, 1), "FIRST_REFUSAL",
		"The first signal refused the attribute set",
		"otel_test: a stand-in for one signal's INVALID_ATTRIBUTE")
	secondRefusal = errs.Define(errs.Pack(0, 2, 0xFE, 2), "SECOND_REFUSAL",
		"The second signal refused the attribute set",
		"otel_test: a stand-in for another signal's INVALID_ATTRIBUTE")
)

// TestAppendText pins the canonical rendering every exporter shares.
//
// The property that matters is not the spelling of any one kind — it is that
// only AttrKindString can produce a byte an exposition format would have to
// escape. A bool, an integer and a double all render from [0-9a-zA-Z+-.], so an
// exporter can escape the string case and append the other three verbatim, and
// the two exporters in this SDK do exactly that.
func TestAppendText(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		attr otel.AttrValue
		want string
	}
	tests := []tc{
		{"a string is its own text", otel.String("k", "GET"), "GET"},
		{"an empty string", otel.String("k", ""), ""},
		{"true", otel.Bool("k", true), "true"},
		{"false", otel.Bool("k", false), "false"},
		{"a positive integer", otel.Int64("k", 503), "503"},
		{"a negative integer", otel.Int64("k", -1), "-1"},
		{"the integer minimum", otel.Int64("k", math.MinInt64), "-9223372036854775808"},
		{"a double", otel.Float64("k", 0.5), "0.5"},
		//: the same shortest-round-trip spelling the Prometheus exposition
		//: format asks for, including its exponent threshold.
		{"a double in exponent notation", otel.Float64("k", 1.7560473e+07), "1.7560473e+07"},
		{"a NaN is named", otel.Float64("k", math.NaN()), "NaN"},
		{"an infinity is named", otel.Float64("k", math.Inf(1)), "+Inf"},
		//: an attribute no constructor built has no value to render, and
		//: renders nothing rather than a forged empty string.
		{"a value no constructor set", otel.AttrValue{Key: "k"}, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(c.attr.AppendText(nil)); got != c.want {
			t.Errorf("AppendText = %q, want %q", got, c.want)
		}
		//: it APPENDS — an exporter builds a whole document in one buffer.
		if got := string(c.attr.AppendText([]byte("x"))); got != "x"+c.want {
			t.Errorf("AppendText did not append onto the buffer: %q", got)
		}
		//: and no rendering but a string's can carry a byte a wire format
		//: would need an escape for.
		if c.attr.Kind() != otel.AttrKindString {
			if strings.ContainsAny(c.want, "\\\"\n") {
				t.Errorf("a %d-kind attribute rendered an escapable byte: %q", c.attr.Kind(), c.want)
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

// TestAppendIdentityIsInjective pins the property a series key rests on: two
// attributes that are not the same value never encode to the same bytes.
//
// The cross-KIND cases are the ones typed attributes introduced. Before them a
// value was a string and length prefixes were enough; now String("v", "1") and
// Int64("v", 1) render identically on any wire that has one value type, so
// without the kind tag they would silently become one series.
func TestAppendIdentityIsInjective(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		attr otel.AttrValue
	}
	tests := []tc{
		{"the string one", otel.String("v", "1")},
		{"the integer one", otel.Int64("v", 1)},
		{"the double one", otel.Float64("v", 1)},
		{"the boolean true", otel.Bool("v", true)},
		{"the string true", otel.String("v", "true")},
		{"the boolean false", otel.Bool("v", false)},
		{"the integer zero", otel.Int64("v", 0)},
		{"positive zero", otel.Float64("v", 0)},
		{"negative zero", otel.Float64("v", math.Copysign(0, -1))},
		{"the empty string", otel.String("v", "")},
		//: a string that spells what a fixed-width encoding of another kind
		//: would emit, byte for byte.
		{"a string of eight zero bytes", otel.String("v", "\x00\x00\x00\x00\x00\x00\x00\x00")},
	}
	seen := make(map[string]string, len(tests))
	for _, c := range tests {
		key := string(c.attr.AppendIdentity(nil))
		if other, clash := seen[key]; clash {
			t.Errorf("%q and %q encode identically to %q", c.name, other, key)
		}
		seen[key] = c.name
	}
	//: and the encoding appends rather than replacing.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			bare := string(c.attr.AppendIdentity(nil))
			if got := string(c.attr.AppendIdentity([]byte("p"))); got != "p"+bare {
				t.Errorf("AppendIdentity did not append onto the buffer: %q", got)
			}
		})
	}
}

// recoverRefusal runs fn and returns the message it panicked with — every
// refusal here panics with a sentinel's message, a string — or "" when it did
// not panic, or panicked with something that is not a message.
func recoverRefusal(fn func()) (message string) {
	defer func() {
		message, _ = recover().(string)
	}()
	fn()
	return ""
}

// TestEveryRefusalCarriesTheCallersCode pins the seam this package exists
// around: it owns no code, so an unusable attribute set panics with exactly the
// message of the sentinel its CALLER passed — and two callers passing two
// sentinels get two different panics for the same defect. That is what lets
// metrics keep INVALID_ATTRIBUTE at 0.2.9.4 while trace names its own.
func TestEveryRefusalCarriesTheCallersCode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []otel.AttrValue
	}
	tests := []tc{
		{"an empty key", []otel.AttrValue{otel.String("", "x")}},
		{"a value no constructor set", []otel.AttrValue{{Key: "host.name"}}},
		{"a repeated key", []otel.AttrValue{otel.String("a", "1"), otel.String("a", "2")}},
		//: only the KIND differs, which is exactly the pair the identity
		//: encoding keeps apart — so it is still a repeated key.
		{"one key under two kinds", []otel.AttrValue{otel.String("a", "1"), otel.Int64("a", 1)}},
	}
	//: the three ways in, each handed both refusals.
	entries := map[string]func(attrs []otel.AttrValue, refusal error){
		"SortAttrs": func(attrs []otel.AttrValue, refusal error) {
			otel.SortAttrs(attrs, refusal)
		},
		"ValidateAttrs": func(attrs []otel.AttrValue, refusal error) {
			//: sorted by hand, so the only call that can refuse is the one
			//: under test.
			sorted := slices.Clone(attrs)
			slices.SortFunc(sorted, otel.CompareAttrKey)
			otel.ValidateAttrs(sorted, refusal)
		},
		"NormalizeResource": func(attrs []otel.AttrValue, refusal error) {
			otel.NormalizeResource(otel.ResourceValue{Attrs: attrs}, refusal)
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		for entry, call := range entries {
			for _, refusal := range []error{firstRefusal, secondRefusal} {
				got := recoverRefusal(func() { call(c.in, refusal) })
				if got != refusal.Error() {
					t.Errorf("%s panicked with %v, want the caller's %q", entry, got, refusal.Error())
				}
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

// TestAUsableSetNeverReadsTheRefusal pins the other half of the seam: a usable
// set is accepted whatever sentinel rides along, including none — the refusal
// is consulted only to panic.
func TestAUsableSetNeverReadsTheRefusal(t *testing.T) {
	t.Parallel()
	attrs := []otel.AttrValue{otel.Int64("b", 2), otel.String("a", "1")}
	sorted := otel.SortAttrs(attrs, nil)
	if len(sorted) != 2 || sorted[0].Key != "a" || sorted[1].Key != "b" {
		t.Fatalf("SortAttrs = %v, want a then b", sorted)
	}
	//: the result owns its backing array; the caller's order is untouched.
	if attrs[0].Key != "b" {
		t.Error("SortAttrs sorted the caller's own slice")
	}
	otel.ValidateAttrs(sorted, nil)
	if otel.SortAttrs(nil, nil) != nil {
		t.Error("an empty set must stay nil — that is how no dimensions is spelled")
	}
}

// TestNormalizeResource pins the producer identity a payload carries once.
//
// The service.name default is the OpenTelemetry specification's own — it
// mandates unknown_service for exactly this case — so the clamp substitutes
// nobody's judgement (ADR 0031). What it must NOT do is overwrite a name the
// caller supplied.
func TestNormalizeResource(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          []otel.AttrValue
		wantKeys    []string
		wantService string
	}
	tests := []tc{
		{
			name:     "an empty resource takes the mandated default",
			wantKeys: []string{otel.ServiceNameKey}, wantService: otel.UnknownService,
		},
		{
			name:     "a declared service name is kept",
			in:       []otel.AttrValue{otel.String(otel.ServiceNameKey, "orders")},
			wantKeys: []string{otel.ServiceNameKey}, wantService: "orders",
		},
		{
			//: sorted by key, and the default lands in its sorted position
			//: rather than at the end.
			name: "other attributes are sorted and the default inserted in order",
			in: []otel.AttrValue{
				otel.String("host.name", "box-1"),
				otel.Int64("process.pid", 42),
			},
			wantKeys:    []string{"host.name", "process.pid", otel.ServiceNameKey},
			wantService: otel.UnknownService,
		},
		{
			name: "a declared name among others",
			in: []otel.AttrValue{
				otel.String(otel.ServiceNameKey, "orders"),
				otel.String("host.name", "box-1"),
			},
			wantKeys:    []string{"host.name", otel.ServiceNameKey},
			wantService: "orders",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := otel.NormalizeResource(otel.ResourceValue{Attrs: c.in}, firstRefusal)
		if len(got.Attrs) != len(c.wantKeys) {
			t.Fatalf("NormalizeResource carries %v, want keys %v", got.Attrs, c.wantKeys)
		}
		for i, key := range c.wantKeys {
			if got.Attrs[i].Key != key {
				t.Errorf("attribute %d is %q, want %q", i, got.Attrs[i].Key, key)
			}
		}
		for _, a := range got.Attrs {
			if a.Key == otel.ServiceNameKey && a.Str() != c.wantService {
				t.Errorf("service.name = %q, want %q", a.Str(), c.wantService)
			}
		}
		//: the result owns its backing array — a caller who keeps their slice
		//: and mutates it must not be able to rewrite a published Resource.
		if len(c.in) > 0 && len(got.Attrs) > 0 && &c.in[0] == &got.Attrs[0] {
			t.Error("NormalizeResource aliased the caller's own slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNormalizeScope pins the instrumentation identity. An empty Name takes
// the default the SIGNAL passes — the library that produced it — and an empty
// Version STAYS empty, because the specification makes it optional and
// inventing one would be a claim about code this package cannot see.
func TestNormalizeScope(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          otel.ScopeValue
		wantName    string
		wantVersion string
	}
	tests := []tc{
		{"an empty scope takes the signal's default", otel.ScopeValue{}, testDefaultScope, ""},
		{
			"a version without a name still gets the default name",
			otel.ScopeValue{Version: "1.4.0"},
			testDefaultScope, "1.4.0",
		},
		{
			"a declared scope is kept whole",
			otel.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"},
			"github.com/acme/orders", "1.4.0",
		},
		{
			"a declared name without a version keeps the absence",
			otel.ScopeValue{Name: "github.com/acme/orders"},
			"github.com/acme/orders", "",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := otel.NormalizeScope(c.in, testDefaultScope)
		if got.Name != c.wantName {
			t.Errorf("Name = %q, want %q", got.Name, c.wantName)
		}
		if got.Version != c.wantVersion {
			t.Errorf("Version = %q, want %q", got.Version, c.wantVersion)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
