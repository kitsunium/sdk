package slogbridge

import (
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// The two scales share their integers by construction, so the conversion has
// to be exact rather than merely order-preserving. Reached directly here: an
// external test can only observe it through a recorded record, which cannot
// show what happens at the int8 boundary.
func Test_toLevel(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   slog.Level
		want logger.Level
	}
	tests := []tc{
		{"debug", slog.LevelDebug, logger.LevelDebug},
		{"info", slog.LevelInfo, logger.LevelInfo},
		{"warn", slog.LevelWarn, logger.LevelWarn},
		{"error", slog.LevelError, logger.LevelError},
		{"a custom level keeps its position", 2, 2},
		{"the highest representable level", math.MaxInt8, math.MaxInt8},
		{"the lowest representable level", math.MinInt8, math.MinInt8},
		// Saturation, not wrapping: a wrap would turn an absurdly severe
		// level into a debug one, which is the dangerous direction.
		{"just above the range saturates high", math.MaxInt8 + 1, math.MaxInt8},
		{"far above saturates high", 100000, math.MaxInt8},
		{"just below the range saturates low", math.MinInt8 - 1, math.MinInt8},
		{"far below saturates low", -100000, math.MinInt8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := toLevel(c.in); got != c.want {
			t.Errorf("toLevel(%d) = %d, want %d", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// An empty group NAME inlines its members — the group adds no level.
func Test_qualifyGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		group  string
		want   string
	}
	tests := []tc{
		{"no chain yet", "", "g", "g"},
		{"extends the chain", "g1", "g2", "g1.g2"},
		{"an empty name inlines into the parent", "g1", "", "g1"},
		{"an empty name with no chain stays empty", "", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := qualifyGroup(c.prefix, c.group); got != c.want {
			t.Errorf("qualifyGroup(%q, %q) = %q, want %q", c.prefix, c.group, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// An empty KEY is the opposite case: slog prints "g.=v", keeping the
// separator so the record still shows which group the value came from.
func Test_qualifyKey(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		prefix string
		key    string
		want   string
	}
	tests := []tc{
		{"no chain leaves the key bare", "", "k", "k"},
		{"joins the chain", "g", "k", "g.k"},
		{"a nested chain is preserved whole", "g1.g2", "k", "g1.g2.k"},
		{"an empty key keeps the separator", "g", "", "g."},
		{"an empty key with no chain stays empty", "", "", ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := qualifyKey(c.prefix, c.key); got != c.want {
			t.Errorf("qualifyKey(%q, %q) = %q, want %q", c.prefix, c.key, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Each slog Kind must land on its SDK peer, and KindAny must NOT — both
// encoders print "?" for it, so the value would reach the log erased.
func Test_convert(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   slog.Value
		want logger.Kind
	}
	tests := []tc{
		{"string", slog.StringValue("v"), logger.KindString},
		{"int64", slog.Int64Value(1), logger.KindInt64},
		{"uint64", slog.Uint64Value(1), logger.KindUint64},
		{"float64", slog.Float64Value(1.5), logger.KindFloat64},
		{"bool", slog.BoolValue(true), logger.KindBool},
		{"duration", slog.DurationValue(time.Second), logger.KindDuration},
		{"time", slog.TimeValue(time.Unix(0, 0)), logger.KindTime},
		{"any becomes text, never KindAny", slog.AnyValue([]int{1}), logger.KindString},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := convert("k", c.in)
		if got.Key != "k" {
			t.Errorf("key = %q, want %q", got.Key, "k")
		}
		if got.Value.Kind() != c.want {
			t.Errorf("kind = %v, want %v", got.Value.Kind(), c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// slog's elision rules live in the bridge, since the SDK handler has no
// notion of them.
func Test_appendAttr(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		prefix   string
		attr     slog.Attr
		wantKeys []string
	}
	tests := []tc{
		{"a leaf keeps its key", "", slog.String("k", "v"), []string{"k"}},
		{"a leaf under a chain is qualified", "g", slog.String("k", "v"), []string{"g.k"}},
		{"a zero attr is dropped", "", slog.Attr{}, nil},
		{"a LogValuer is resolved before conversion", "", slog.Any("k", resolver{}), []string{"k"}},
		{
			"a group is expanded", "",
			slog.Group("g", slog.String("a", "1")),
			[]string{"g.a"},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		assertKeys(t, appendAttr(nil, c.prefix, c.attr), c.wantKeys)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Groups flatten to dotted keys because neither bundled encoder renders a
// group payload — an unflattened group would print as "?".
func Test_appendGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		prefix   string
		group    string
		members  []slog.Attr
		wantKeys []string
	}
	tests := []tc{
		{
			"members are flattened under the name", "", "g",
			[]slog.Attr{slog.String("a", "1"), slog.String("b", "2")},
			[]string{"g.a", "g.b"},
		},
		{
			"a nested group keeps the whole chain", "", "g1",
			[]slog.Attr{slog.Group("g2", slog.String("k", "v"))},
			[]string{"g1.g2.k"},
		},
		{
			"an existing chain is extended", "outer", "g",
			[]slog.Attr{slog.String("k", "v")},
			[]string{"outer.g.k"},
		},
		{
			"an empty name inlines the members", "", "",
			[]slog.Attr{slog.String("k", "v")},
			[]string{"k"},
		},
		{"a group with no members is dropped", "", "g", nil, nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		assertKeys(t, appendGroup(nil, c.prefix, c.group, c.members), c.wantKeys)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertKeys compares the produced attribute keys against what a case expects.
func assertKeys(t *testing.T, got []logger.Attr, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("attrs = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, key := range want {
		if got[i].Key != key {
			t.Errorf("key[%d] = %q, want %q", i, got[i].Key, key)
		}
	}
}

// resolver stands in for a consumer type implementing slog.LogValuer.
type resolver struct{}

// LogValue satisfies slog.LogValuer.
func (resolver) LogValue() slog.Value { return slog.StringValue("resolved") }
