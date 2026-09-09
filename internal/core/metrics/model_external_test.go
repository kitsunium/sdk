// Package metrics_test — the OTel data-model values the port declares:
// typed attributes, aggregation temporality, Resource and InstrumentationScope.
package metrics_test

import (
	"math"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/metrics"
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
		attr metrics.AttrValue
		want string
	}
	tests := []tc{
		{"a string is its own text", metrics.String("k", "GET"), "GET"},
		{"an empty string", metrics.String("k", ""), ""},
		{"true", metrics.Bool("k", true), "true"},
		{"false", metrics.Bool("k", false), "false"},
		{"a positive integer", metrics.Int64("k", 503), "503"},
		{"a negative integer", metrics.Int64("k", -1), "-1"},
		{"the integer minimum", metrics.Int64("k", math.MinInt64), "-9223372036854775808"},
		{"a double", metrics.Float64("k", 0.5), "0.5"},
		//: the same shortest-round-trip spelling the Prometheus exposition
		//: format asks for, including its exponent threshold.
		{"a double in exponent notation", metrics.Float64("k", 1.7560473e+07), "1.7560473e+07"},
		{"a NaN is named", metrics.Float64("k", math.NaN()), "NaN"},
		{"an infinity is named", metrics.Float64("k", math.Inf(1)), "+Inf"},
		//: an attribute no constructor built has no value to render, and
		//: renders nothing rather than a forged empty string.
		{"a value no constructor set", metrics.AttrValue{Key: "k"}, ""},
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
		if c.attr.Kind() != metrics.AttrKindString {
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
		attr metrics.AttrValue
	}
	tests := []tc{
		{"the string one", metrics.String("v", "1")},
		{"the integer one", metrics.Int64("v", 1)},
		{"the double one", metrics.Float64("v", 1)},
		{"the boolean true", metrics.Bool("v", true)},
		{"the string true", metrics.String("v", "true")},
		{"the boolean false", metrics.Bool("v", false)},
		{"the integer zero", metrics.Int64("v", 0)},
		{"positive zero", metrics.Float64("v", 0)},
		{"negative zero", metrics.Float64("v", math.Copysign(0, -1))},
		{"the empty string", metrics.String("v", "")},
		//: a string that spells what a fixed-width encoding of another kind
		//: would emit, byte for byte.
		{"a string of eight zero bytes", metrics.String("v", "\x00\x00\x00\x00\x00\x00\x00\x00")},
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

// TestTemporalityResolved pins ADR 0031 on the concept the OTel model exists to
// make explicit.
//
// Unset CLAMPS to cumulative, and the reason is not convention: an in-memory
// meter accumulates into atomics and never resets them, so "cumulative" is a
// DESCRIPTION of what an unconfigured meter does rather than a value chosen on
// the caller's behalf. A value that is none of the three constants can only
// come from a cast, so it REFUSES.
func TestTemporalityResolved(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		in        metrics.Temporality
		want      metrics.Temporality
		wantPanic bool
	}
	tests := []tc{
		{name: "unset clamps to cumulative", in: metrics.TemporalityUnspecified, want: metrics.TemporalityCumulative},
		{name: "cumulative is honoured", in: metrics.TemporalityCumulative, want: metrics.TemporalityCumulative},
		{name: "delta is honoured", in: metrics.TemporalityDelta, want: metrics.TemporalityDelta},
		{name: "a cast value refuses", in: metrics.Temporality(7), wantPanic: true},
		{name: "the maximum cast value refuses", in: metrics.Temporality(255), wantPanic: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if !c.wantPanic {
				if r != nil {
					t.Errorf("Resolved panicked: %v", r)
				}
				return
			}
			if r == nil {
				t.Fatal("a cast temporality did not refuse")
			}
			msg, isString := r.(string)
			if !isString || msg != metrics.InvalidTemporality.Error() {
				t.Errorf("the panic value is %v, want the InvalidTemporality message", r)
			}
		}()
		if got := c.in.Resolved(); got != c.want {
			t.Errorf("Resolved() = %v, want %v", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestTemporalityString pins the spellings the text exporter emits verbatim.
func TestTemporalityString(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   metrics.Temporality
		want string
	}
	tests := []tc{
		{"unspecified", metrics.TemporalityUnspecified, "unspecified"},
		{"delta", metrics.TemporalityDelta, "delta"},
		{"cumulative", metrics.TemporalityCumulative, "cumulative"},
		{"a cast value", metrics.Temporality(7), "unspecified"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.in.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestResourceNormalized pins the producer identity a snapshot carries once.
//
// The service.name default is the OpenTelemetry specification's own — it
// mandates unknown_service for exactly this case — so the clamp substitutes
// nobody's judgement (ADR 0031). What it must NOT do is overwrite a name the
// caller supplied.
func TestResourceNormalized(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          []metrics.AttrValue
		wantKeys    []string
		wantService string
	}
	tests := []tc{
		{
			name:     "an empty resource takes the mandated default",
			wantKeys: []string{metrics.ServiceNameKey}, wantService: metrics.UnknownService,
		},
		{
			name:     "a declared service name is kept",
			in:       []metrics.AttrValue{metrics.String(metrics.ServiceNameKey, "orders")},
			wantKeys: []string{metrics.ServiceNameKey}, wantService: "orders",
		},
		{
			//: sorted by key, and the default lands in its sorted position
			//: rather than at the end.
			name: "other attributes are sorted and the default inserted in order",
			in: []metrics.AttrValue{
				metrics.String("host.name", "box-1"),
				metrics.Int64("process.pid", 42),
			},
			wantKeys:    []string{"host.name", "process.pid", metrics.ServiceNameKey},
			wantService: metrics.UnknownService,
		},
		{
			name: "a declared name among others",
			in: []metrics.AttrValue{
				metrics.String(metrics.ServiceNameKey, "orders"),
				metrics.String("host.name", "box-1"),
			},
			wantKeys:    []string{"host.name", metrics.ServiceNameKey},
			wantService: "orders",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := metrics.ResourceValue{Attrs: c.in}.Normalized()
		if len(got.Attrs) != len(c.wantKeys) {
			t.Fatalf("Normalized carries %v, want keys %v", got.Attrs, c.wantKeys)
		}
		for i, key := range c.wantKeys {
			if got.Attrs[i].Key != key {
				t.Errorf("attribute %d is %q, want %q", i, got.Attrs[i].Key, key)
			}
		}
		for _, a := range got.Attrs {
			if a.Key == metrics.ServiceNameKey && a.Str() != c.wantService {
				t.Errorf("service.name = %q, want %q", a.Str(), c.wantService)
			}
		}
		//: the result owns its backing array — a caller who keeps their slice
		//: and mutates it must not be able to rewrite a published Resource.
		if len(c.in) > 0 && len(got.Attrs) > 0 && &c.in[0] == &got.Attrs[0] {
			t.Error("Normalized aliased the caller's own slice")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestResourceNormalizedRefusesAnUnusableAttribute pins that a resource is
// validated at construction rather than at the first export. A resource is
// written once at wiring time, so a bad key there is wrong on the first run or
// never — the same argument the meter makes about an attribute key.
func TestResourceNormalizedRefusesAnUnusableAttribute(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []metrics.AttrValue
	}
	tests := []tc{
		{"an empty key", []metrics.AttrValue{metrics.String("", "x")}},
		{"a value no constructor set", []metrics.AttrValue{{Key: "host.name"}}},
		{"a repeated key", []metrics.AttrValue{metrics.String("a", "1"), metrics.String("a", "2")}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Error("an unusable resource attribute did not refuse")
			}
		}()
		metrics.ResourceValue{Attrs: c.in}.Normalized()
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestScopeNormalized pins the instrumentation identity. An empty Name takes
// the one truthful default — the library that minted the instruments — and an
// empty Version STAYS empty, because the specification makes it optional and
// inventing one would be a claim about code this package cannot see.
func TestScopeNormalized(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          metrics.ScopeValue
		wantName    string
		wantVersion string
	}
	tests := []tc{
		{"an empty scope names this SDK", metrics.ScopeValue{}, metrics.DefaultScopeName, ""},
		{
			"a version without a name still gets the default name",
			metrics.ScopeValue{Version: "1.4.0"},
			metrics.DefaultScopeName, "1.4.0",
		},
		{
			"a declared scope is kept whole",
			metrics.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"},
			"github.com/acme/orders", "1.4.0",
		},
		{
			"a declared name without a version keeps the absence",
			metrics.ScopeValue{Name: "github.com/acme/orders"},
			"github.com/acme/orders", "",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := c.in.Normalized()
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
