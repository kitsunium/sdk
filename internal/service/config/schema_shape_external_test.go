// Package config_test — the schema's KEY pass: which keys a load must find,
// which it must refuse, and why the two are decided before the decode.
package config_test

import (
	"strings"
	"testing"
	"time"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"
)

// shapeDatabase is the nested table every key case addresses through, so a
// missing or unknown key exercises the dotted grammar rather than a flat name.
type shapeDatabase struct {
	DSN      string `json:"dsn"`
	MaxConns int    `json:"max_conns"`
}

// shapeConf is the target of the key pass. Timeout carries no `required` tag on
// purpose: the point of several cases is that a KEY requirement and a VALUE
// requirement are different statements.
type shapeConf struct {
	Port     int            `json:"port"    validate:"min=1,max=65535"`
	Timeout  int            `json:"timeout" validate:"min=0,max=3600"`
	Token    string         `json:"token"`
	Database shapeDatabase  `json:"database"`
	Labels   map[string]any `json:"labels"`
	Born     time.Time      `json:"born"`
}

// newShapeSchema builds a schema from spec, failing the test on refusal.
func newShapeSchema(tb testing.TB, spec cfg.SchemaSpec[shapeConf]) *cfg.SchemaValue[shapeConf] {
	tb.Helper()
	schema, err := cfg.NewSchemaValue[shapeConf](spec)
	if err != nil {
		tb.Fatalf("NewSchema: %v", err)
	}
	return schema
}

// TestARequiredKeyMissingFailsTheLoadNotTheFirstAccess is the reason a schema
// exists at all. The key is declared required, no layer supplies it, and the
// failure happens at LoadSchema — before the process has a configuration to run
// on, rather than on the first request that happens to read the field.
func TestARequiredKeyMissingFailsTheLoadNotTheFirstAccess(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"database.dsn"},
	})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":     8080,
		"database": map[string]any{"max_conns": 8},
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a configuration missing a required key")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigKeyMissing) {
		t.Errorf("code = %v, want CONFIG_KEY_MISSING", err)
	}
	if keys := fieldValue(t, err, "keys"); keys != "database.dsn" {
		t.Errorf("keys field = %q, want database.dsn", keys)
	}
}

// TestEveryMissingKeyIsNamedInOneError is ADR 0046's collect-all rule, applied
// to keys. Reporting the first would make an operator restart the service once
// per missing key to discover the next — the failure a report exists to
// prevent.
func TestEveryMissingKeyIsNamedInOneError(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"port", "token", "database.dsn"},
	})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"database": map[string]any{"max_conns": 8},
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a configuration missing three required keys")
	}
	if count := fieldValue(t, err, "missing"); count != "3" {
		t.Errorf("missing field = %q, want 3", count)
	}
	//: declaration order, so the message reads in the order it was written.
	if keys := fieldValue(t, err, "keys"); keys != "port, token, database.dsn" {
		t.Errorf("keys field = %q, want all three in declaration order", keys)
	}
}

// TestARequiredKeySetToItsZeroIsSupplied draws the line this domain exists to
// keep. Required is a KEY-level statement decided on the merged map; the
// validation domain's `required` is a VALUE-level statement decided after the
// decode, where an absent key and an explicit zero are the same bytes. An
// operator who deliberately writes `timeout = 0` has supplied the key.
func TestARequiredKeySetToItsZeroIsSupplied(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"timeout"},
	})

	var conf shapeConf
	if err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":    8080,
		"timeout": 0,
	}}); err != nil {
		t.Fatalf("LoadSchema refused an explicitly-zero required key: %v", err)
	}
	if conf.Timeout != 0 {
		t.Errorf("timeout = %d, want the operator's 0", conf.Timeout)
	}
}

// TestAMissingKeyIsNotAlsoReportedAsABadValue is why the two passes are not
// merged into one report. A key nobody supplied decodes to a zero the operator
// never wrote; running the bounds on that zero names a rule nobody violated and
// buries the one actionable fact under a fact derived from it.
func TestAMissingKeyIsNotAlsoReportedAsABadValue(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"port"},
	})

	var conf shapeConf
	//: port is absent, so the min=1 tag would fail on the decoded 0 too.
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"database": map[string]any{"max_conns": 8},
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a configuration missing a required key")
	}
	if errs.HasCode(err, coreconfig.CodeConfigValidationFailed) {
		t.Errorf("a missing key was also reported as a value violation: %v", err)
	}
}

// TestAnUnknownKeyIsRefusedByDefault is the incident the check exists for: a
// typo in an environment variable that decodes into nothing, leaves the process
// on its old setting, and produces no evidence but the absence of an effect.
func TestAnUnknownKeyIsRefusedByDefault(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":  8080,
		"portt": 9090,
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a key no field of the target addresses")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigUnknownKey) {
		t.Errorf("code = %v, want CONFIG_UNKNOWN_KEY", err)
	}
	if keys := fieldValue(t, err, "keys"); keys != "portt" {
		t.Errorf("keys field = %q, want portt", keys)
	}
}

// TestUnknownKeysAreAllNamedAndOrdered collects them all — and sorts them. A
// map iterates randomly, so an unsorted message would differ between two runs
// of the same deployment: undiffable in a log and unpinnable in a test.
func TestUnknownKeysAreAllNamedAndOrdered(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{})

	//: run it repeatedly: one pass could be sorted by luck.
	for range 8 {
		var conf shapeConf
		err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
			"port":     8080,
			"zeta":     1,
			"alpha":    2,
			"database": map[string]any{"max_conns": 8, "dsnn": "x"},
		}})
		if err == nil {
			t.Fatal("LoadSchema accepted three unaddressable keys")
		}
		if count := fieldValue(t, err, "unknown"); count != "3" {
			t.Fatalf("unknown field = %q, want 3", count)
		}
		if keys := fieldValue(t, err, "keys"); keys != "alpha, database.dsnn, zeta" {
			t.Fatalf("keys field = %q, want the three sorted", keys)
		}
	}
}

// TestAllowUnknownKeysOptsOutByName covers the legitimate case: a file shared
// by two services, or an unprefixed environment. The escape hatch exists, and
// it is spelled at the wiring site rather than guessed at load time.
func TestAllowUnknownKeysOptsOutByName(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{AllowUnknownKeys: true})

	var conf shapeConf
	if err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":            8080,
		"another_service": map[string]any{"port": 9090},
	}}); err != nil {
		t.Fatalf("LoadSchema refused an extra key the schema allows: %v", err)
	}
	if conf.Port != 8080 {
		t.Errorf("port = %d, want 8080", conf.Port)
	}
}

// TestTheUnknownWalkStopsAtALeaf is the property that keeps the check from
// firing on legitimate data. Below a map[string]any and below a type that
// decodes itself there are no keys — there is one value the decoder owns — so
// descending would report a document's contents as a typo.
func TestTheUnknownWalkStopsAtALeaf(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{})

	var conf shapeConf
	if err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":   8080,
		"labels": map[string]any{"team": "core", "nested": map[string]any{"x": 1}},
		"born":   "2026-09-10T00:00:00Z",
	}}); err != nil {
		t.Fatalf("LoadSchema descended into a leaf: %v", err)
	}
	if conf.Labels["team"] != "core" {
		t.Errorf("labels[team] = %v, want core", conf.Labels["team"])
	}
}

// TestBothKeyFailuresTravelTogether: they are independent facts about the same
// map, so an operator who fixes the missing keys only to be told about the typo
// on the next restart has paid twice for one pass. errors.Join keeps both
// matchable through errs.HasCode.
func TestBothKeyFailuresTravelTogether(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"database.dsn"},
	})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":  8080,
		"portt": 9090,
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a missing key and an unknown one")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigKeyMissing) {
		t.Errorf("CONFIG_KEY_MISSING not matchable on the joined error: %v", err)
	}
	if !errs.HasCode(err, coreconfig.CodeConfigUnknownKey) {
		t.Errorf("CONFIG_UNKNOWN_KEY not matchable on the joined error: %v", err)
	}
	//: both legs must be visible in the rendered text, or the join has hidden
	//: half the diagnosis behind a code the operator has to look up.
	if text := err.Error(); !strings.Contains(text, "CONFIG_KEY_MISSING") ||
		!strings.Contains(text, "CONFIG_UNKNOWN_KEY") {
		t.Errorf("error text = %q, want both legs", text)
	}
}

// TestARequiredKeyIsNeverAlsoDefaulted is ADR 0031 applied to a pair of
// clauses rather than to a zero value. The schema would fill the key itself, so
// the requirement could never fire — and a clause that cannot fire is worse
// than no clause, because a reader takes it for protection.
func TestARequiredKeyIsNeverAlsoDefaulted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec cfg.SchemaSpec[shapeConf]
	}{
		{
			name: "the same key",
			spec: cfg.SchemaSpec[shapeConf]{
				Required: []string{"port"},
				Defaults: []coreconfig.DeclaredValue{{Key: "port", Value: 8080}},
			},
		},
		{
			name: "a default under a required table",
			spec: cfg.SchemaSpec[shapeConf]{
				Required: []string{"database"},
				Defaults: []coreconfig.DeclaredValue{{Key: "database.max_conns", Value: 16}},
			},
		},
		{
			name: "a required key under a defaulted table",
			spec: cfg.SchemaSpec[shapeConf]{
				Required: []string{"database.max_conns"},
				Defaults: []coreconfig.DeclaredValue{
					{Key: "database", Value: map[string]any{"dsn": "x", "max_conns": 16}},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := cfg.NewSchemaValue[shapeConf](tc.spec)
			if err == nil {
				t.Fatal("NewSchema accepted a requirement that can never fire")
			}
			if !errs.HasCode(err, coreconfig.CodeConfigSchemaInvalid) {
				t.Errorf("code = %v, want CONFIG_SCHEMA_INVALID", err)
			}
			if clause := fieldValue(t, err, "clause"); !strings.Contains(clause, "required") {
				t.Errorf("clause = %q, want the required/default contradiction", clause)
			}
		})
	}
}

// TestARequiredKeyMustNameSomething closes the other construction-time hole: a
// typo in a REQUIREMENT would produce a clause that can never be satisfied,
// which fails every deployment rather than none — loud, but for the wrong
// reason and at the wrong site.
func TestARequiredKeyMustNameSomething(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		required []string
		wantKey  string
	}{
		{name: "no such field", required: []string{"database.dsnn"}, wantKey: "database.dsnn"},
		{name: "outside the grammar", required: []string{"database..dsn"}, wantKey: "database..dsn"},
		{name: "empty", required: []string{""}, wantKey: ""},
		{name: "declared twice", required: []string{"port", "port"}, wantKey: "port"},
		{name: "under a leaf", required: []string{"labels.team"}, wantKey: "labels.team"},
		{name: "inside a self-decoding type", required: []string{"born.wall"}, wantKey: "born.wall"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := cfg.NewSchemaValue[shapeConf](cfg.SchemaSpec[shapeConf]{Required: tc.required})
			if err == nil {
				t.Fatal("NewSchema accepted a requirement that names nothing")
			}
			if !errs.HasCode(err, coreconfig.CodeConfigSchemaInvalid) {
				t.Errorf("code = %v, want CONFIG_SCHEMA_INVALID", err)
			}
			if key := fieldValue(t, err, "key"); key != tc.wantKey {
				t.Errorf("key field = %q, want %q", key, tc.wantKey)
			}
		})
	}
}

// TestAKeyFailureNeverEchoesAValue is the security property, on the key pass
// this time. A missing key has no value, but the map it was looked for in is
// full of them — and an unknown key is frequently a misspelled SECRET, whose
// value is right there next to the name that was refused.
func TestAKeyFailureNeverEchoesAValue(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"database.dsn"},
	})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{
		"port":  8080,
		"token": secretProbe,
		//: the misspelling that carries the secret as its value.
		"tokenn": secretProbe,
	}})
	if err == nil {
		t.Fatal("LoadSchema accepted a missing key and a misspelled one")
	}
	assertNoProbe(t, "error text", err.Error())
	for _, field := range errs.FieldsOf(err) {
		assertNoProbe(t, "error field "+field.Key(), field.StringValue())
	}
}

// TestNoSchemaMeansNoKeyPass keeps the older contract intact. Load without a
// schema declares nothing, so it requires nothing and refuses nothing — the
// reason the schema arrived as a second entry point rather than as a widened
// signature nobody could opt out of.
func TestNoSchemaMeansNoKeyPass(t *testing.T) {
	t.Parallel()
	var conf shapeConf
	if err := cfg.Load(&conf, mapSource{values: map[string]any{
		"port":  8080,
		"portt": 9090,
	}}); err != nil {
		t.Fatalf("Load refused an unknown key without a schema: %v", err)
	}
	if conf.Port != 8080 {
		t.Errorf("port = %d, want 8080", conf.Port)
	}
}

// TestADefaultSatisfiesNothingItWasNotDeclaredFor guards the interaction the
// two features have with each other: the default LAYER is merged before the key
// pass runs, so a defaulted key is present — which is exactly why it may not
// also be required, and why a key that is merely a SIBLING of a defaulted one
// is still missing.
func TestADefaultSatisfiesNothingItWasNotDeclaredFor(t *testing.T) {
	t.Parallel()
	schema := newShapeSchema(t, cfg.SchemaSpec[shapeConf]{
		Required: []string{"database.dsn"},
		Defaults: []coreconfig.DeclaredValue{{Key: "database.max_conns", Value: 16}},
	})

	var conf shapeConf
	err := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{"port": 8080}})
	if err == nil {
		t.Fatal("a default for a sibling key satisfied the requirement")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigKeyMissing) {
		t.Errorf("code = %v, want CONFIG_KEY_MISSING", err)
	}
}
