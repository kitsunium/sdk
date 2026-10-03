// Package metrics_test — the OTel data-model values this signal owns:
// aggregation temporality, and its half of the shared Resource and
// InstrumentationScope (internal/core/observe/otel holds the types and their rules).
package metrics_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/core/observe/metrics"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
)

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

// TestNormalizeResource pins the producer identity a snapshot carries once.
//
// The service.name default is the OpenTelemetry specification's own — it
// mandates unknown_service for exactly this case — so the clamp substitutes
// nobody's judgement (ADR 0031). What it must NOT do is overwrite a name the
// caller supplied.
func TestNormalizeResource(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          []coreotel.AttrValue
		wantKeys    []string
		wantService string
	}
	tests := []tc{
		{
			name:     "an empty resource takes the mandated default",
			wantKeys: []string{coreotel.ServiceNameKey}, wantService: coreotel.UnknownService,
		},
		{
			name:     "a declared service name is kept",
			in:       []coreotel.AttrValue{coreotel.String(coreotel.ServiceNameKey, "orders")},
			wantKeys: []string{coreotel.ServiceNameKey}, wantService: "orders",
		},
		{
			//: sorted by key, and the default lands in its sorted position
			//: rather than at the end.
			name: "other attributes are sorted and the default inserted in order",
			in: []coreotel.AttrValue{
				coreotel.String("host.name", "box-1"),
				coreotel.Int64("process.pid", 42),
			},
			wantKeys:    []string{"host.name", "process.pid", coreotel.ServiceNameKey},
			wantService: coreotel.UnknownService,
		},
		{
			name: "a declared name among others",
			in: []coreotel.AttrValue{
				coreotel.String(coreotel.ServiceNameKey, "orders"),
				coreotel.String("host.name", "box-1"),
			},
			wantKeys:    []string{"host.name", coreotel.ServiceNameKey},
			wantService: "orders",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := metrics.NormalizeResource(coreotel.ResourceValue{Attrs: c.in})
		if len(got.Attrs) != len(c.wantKeys) {
			t.Fatalf("NormalizeResource carries %v, want keys %v", got.Attrs, c.wantKeys)
		}
		for i, key := range c.wantKeys {
			if got.Attrs[i].Key != key {
				t.Errorf("attribute %d is %q, want %q", i, got.Attrs[i].Key, key)
			}
		}
		for _, a := range got.Attrs {
			if a.Key == coreotel.ServiceNameKey && a.Str() != c.wantService {
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

// TestNormalizeResourceRefusesWithInvalidAttribute pins that a resource is
// validated at construction rather than at the first export, and under THIS
// signal's code. A resource is written once at wiring time, so a bad key there
// is wrong on the first run or never — the same argument the meter makes about
// an attribute key. The rules live in internal/core/observe/otel; the code that names
// the defect is still INVALID_ATTRIBUTE, 0.2.9.4, as it was before the model
// moved.
func TestNormalizeResourceRefusesWithInvalidAttribute(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   []coreotel.AttrValue
	}
	tests := []tc{
		{"an empty key", []coreotel.AttrValue{coreotel.String("", "x")}},
		{"a value no constructor set", []coreotel.AttrValue{{Key: "host.name"}}},
		{"a repeated key", []coreotel.AttrValue{coreotel.String("a", "1"), coreotel.String("a", "2")}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("an unusable resource attribute did not refuse")
			}
			if msg, isString := r.(string); !isString || msg != metrics.InvalidAttribute.Error() {
				t.Errorf("the panic value is %v, want the InvalidAttribute message", r)
			}
		}()
		metrics.NormalizeResource(coreotel.ResourceValue{Attrs: c.in})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestNormalizeScope pins the instrumentation identity. An empty Name takes
// the one truthful default — the library that minted the instruments — and an
// empty Version STAYS empty, because the specification makes it optional and
// inventing one would be a claim about code this package cannot see.
func TestNormalizeScope(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		in          coreotel.ScopeValue
		wantName    string
		wantVersion string
	}
	tests := []tc{
		{"an empty scope names this SDK", coreotel.ScopeValue{}, metrics.DefaultScopeName, ""},
		{
			"a version without a name still gets the default name",
			coreotel.ScopeValue{Version: "1.4.0"},
			metrics.DefaultScopeName, "1.4.0",
		},
		{
			"a declared scope is kept whole",
			coreotel.ScopeValue{Name: "github.com/acme/orders", Version: "1.4.0"},
			"github.com/acme/orders", "1.4.0",
		},
		{
			"a declared name without a version keeps the absence",
			coreotel.ScopeValue{Name: "github.com/acme/orders"},
			"github.com/acme/orders", "",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := metrics.NormalizeScope(c.in)
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
