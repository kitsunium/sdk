// Package config_test — the schema: typed defaults, layer precedence, and
// validation located at the operator's key.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	cfg "github.com/kitsunium/sdk/internal/service/config"
	svcvalidation "github.com/kitsunium/sdk/internal/service/validation"

	_ "github.com/kitsunium/sdk/internal/service/codec/json" // register "json"
)

// secretProbe is the value every leak assertion looks for. It is deliberately
// unlike anything else in this file so a substring match cannot be a
// coincidence.
const secretProbe string = "hunter2-Zx9QpL-secret"

// dbSchemaConf is a nested table, so a violation inside it exercises the dotted
// key an operator actually types.
type dbSchemaConf struct {
	Host     string `json:"host"      validate:"required"`
	MaxConns int    `json:"max_conns" validate:"min=1,max=512"`
	Password string `json:"password"  validate:"minlen=12"`
}

// srvSchemaConf is the target every schema case decodes into.
type srvSchemaConf struct {
	Port     int          `json:"port"     validate:"min=1,max=65535"`
	Timeout  int          `json:"timeout"  validate:"min=0,max=3600"`
	Mode     string       `json:"mode"     validate:"oneof=dev|prod"`
	Database dbSchemaConf `json:"database" validate:"dive"`
}

// mapSource is the "explicit override" layer: Source is one method, so a
// caller decides values at the call site without the SDK owning a type for it.
type mapSource struct {
	values map[string]any
}

// Load returns the literal layer.
func (m mapSource) Load() (values map[string]any, err error) {
	//: the caller's own map, handed straight back.
	return m.values, nil
}

// baseSchema builds the schema every precedence case shares: a full set of
// defaults that satisfies every rule the type declares.
func baseSchema(t *testing.T) *cfg.SchemaValue[srvSchemaConf] {
	t.Helper()
	schema, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{
		Defaults: []coreconfig.DeclaredValue{
			{Key: "port", Value: 8080},
			{Key: "timeout", Value: 30},
			{Key: "mode", Value: "prod"},
			{Key: "database.host", Value: "localhost"},
			{Key: "database.max_conns", Value: 16},
			{Key: "database.password", Value: "placeholder-not-a-secret"},
		},
	})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return schema
}

// TestDefaultFillsAnAbsentKeyAndNeverAnExplicitZero is the distinction the
// whole feature exists for. Presence is decided on the merged MAP, while the
// operator's key is still a key — so an omitted timeout takes the default and
// an explicit `timeout = 0` stays zero. A post-decode "if the field is zero,
// default it" cannot tell those two apart, and would overrule the operator on
// every field whose zero value is meaningful.
func TestDefaultFillsAnAbsentKeyAndNeverAnExplicitZero(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)

	//: absent — nobody supplied "timeout".
	var absent srvSchemaConf
	if err := cfg.LoadSchema(&absent, schema); err != nil {
		t.Fatalf("LoadSchema (absent): %v", err)
	}
	if absent.Timeout != 30 {
		t.Errorf("absent key: Timeout = %d, want the default 30", absent.Timeout)
	}

	//: explicitly zero — an operator wrote it, so it must survive.
	var explicit srvSchemaConf
	err := cfg.LoadSchema(&explicit, schema, mapSource{values: map[string]any{"timeout": 0}})
	if err != nil {
		t.Fatalf("LoadSchema (explicit zero): %v", err)
	}
	if explicit.Timeout != 0 {
		t.Errorf("explicit zero: Timeout = %d, want 0 — the default overrode a supplied value", explicit.Timeout)
	}
}

// TestLayerPrecedenceIsDefaultThenFileThenEnvThenOverride walks the full stack
// with real layers — a real file, real environment variables and an explicit
// map — peeling one off at a time. A default must never outrank a source, and
// each source must outrank the one before it.
func TestLayerPrecedenceIsDefaultThenFileThenEnvThenOverride(t *testing.T) {
	schema := baseSchema(t)
	path := filepath.Join(t.TempDir(), "app.json")
	if err := os.WriteFile(path, []byte(`{"port":9090}`), 0o600); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	file := cfg.FileSource("json", path)
	//: EnvSource reads the process environment, so this case cannot be parallel.
	t.Setenv("SCHEMATEST_PORT", "7070")
	env := cfg.EnvSource("SCHEMATEST")
	override := mapSource{values: map[string]any{"port": 6060}}

	cases := []struct {
		name    string
		sources []coreconfig.Source
		want    int
	}{
		{name: "default only", sources: nil, want: 8080},
		{name: "file beats default", sources: []coreconfig.Source{file}, want: 9090},
		{name: "env beats file", sources: []coreconfig.Source{file, env}, want: 7070},
		{name: "override beats env", sources: []coreconfig.Source{file, env, override}, want: 6060},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var conf srvSchemaConf
			if err := cfg.LoadSchema(&conf, schema, tc.sources...); err != nil {
				t.Fatalf("LoadSchema: %v", err)
			}
			if conf.Port != tc.want {
				t.Errorf("Port = %d, want %d", conf.Port, tc.want)
			}
		})
	}
}

// TestAPartiallySuppliedTableKeepsItsOtherDefaults pins the merge property a
// flat "defaults struct overwritten wholesale" implementation gets wrong: a
// file that sets one member of a table must not erase the members it did not
// mention.
func TestAPartiallySuppliedTableKeepsItsOtherDefaults(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	supplied := mapSource{values: map[string]any{
		"database": map[string]any{"host": "db.internal"},
	}}

	var conf srvSchemaConf
	if err := cfg.LoadSchema(&conf, schema, supplied); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if conf.Database.Host != "db.internal" {
		t.Errorf("Database.Host = %q, want the supplied value", conf.Database.Host)
	}
	if conf.Database.MaxConns != 16 {
		t.Errorf("Database.MaxConns = %d, want the default 16 to survive a partial table", conf.Database.MaxConns)
	}
}

// TestAViolationNamesTheOperatorKeyNotTheGoField is requirement three: a
// message an operator can act on names the key they typed. "database.max_conns"
// is greppable in their file; "MaxConns" appears nowhere in it.
func TestAViolationNamesTheOperatorKeyNotTheGoField(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	bad := mapSource{values: map[string]any{
		"database": map[string]any{"max_conns": 0},
	}}

	var conf srvSchemaConf
	err := cfg.LoadSchema(&conf, schema, bad)
	if err == nil {
		t.Fatal("LoadSchema accepted max_conns = 0 against min=1")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigValidationFailed) {
		t.Errorf("code = %v, want CONFIG_VALIDATION_FAILED", err)
	}
	keys := fieldValue(t, err, "keys")
	if keys != "database.max_conns" {
		t.Errorf("keys field = %q, want the operator key %q", keys, "database.max_conns")
	}
	if strings.Contains(keys, "MaxConns") {
		t.Errorf("keys field names the Go field: %q", keys)
	}
}

// TestNoConfigMessageEchoesTheRejectedValue is the security property, inherited
// from the validation domain and re-proven HERE because this is where secrets
// pass: a configuration value is routinely a password, a token or a connection
// string, and a validation message is the one error message designed to reach a
// human. Nothing on any surface — the error text, its fields, or the full
// report — may repeat the value that was refused.
func TestNoConfigMessageEchoesTheRejectedValue(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	//: too short for minlen=12? No — it is long enough, so make it fail the
	//: OTHER way: the probe is supplied where a bounded value is expected.
	leak := mapSource{values: map[string]any{
		"database": map[string]any{"password": "short", "host": secretProbe},
		"mode":     secretProbe,
	}}

	var conf srvSchemaConf
	err := cfg.LoadSchema(&conf, schema, leak)
	if err == nil {
		t.Fatal("LoadSchema accepted an out-of-set mode")
	}
	assertNoProbe(t, "error text", err.Error())
	for _, field := range errs.FieldsOf(err) {
		assertNoProbe(t, "error field "+field.Key(), field.StringValue())
	}

	//: the full report carries messages the error does not — check it too.
	conf.Mode = secretProbe
	conf.Database.Password = secretProbe
	for _, violation := range schema.Check(conf) {
		assertNoProbe(t, "violation message", violation.Message)
		assertNoProbe(t, "violation path", violation.Path)
		assertNoProbe(t, "violation rule", violation.Rule)
	}
}

// TestASchemaRefusalNeverEchoesTheDefaultValue closes the other half of the
// same property. A default is written in source, so it reaches a BUILD log
// rather than a user — and a placeholder credential in source is exactly the
// thing nobody wants printed by CI.
func TestASchemaRefusalNeverEchoesTheDefaultValue(t *testing.T) {
	t.Parallel()
	_, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{
		Defaults: []coreconfig.DeclaredValue{
			//: shorter than minlen=12, so the schema contradicts itself.
			{Key: "database.password", Value: secretProbe[:4]},
		},
	})
	if err == nil {
		t.Fatal("NewSchema accepted a default its own rule rejects")
	}
	assertNoProbe(t, "refusal text", err.Error())
	for _, field := range errs.FieldsOf(err) {
		assertNoProbe(t, "refusal field "+field.Key(), field.StringValue())
	}
	if key := fieldValue(t, err, "key"); key != "database.password" {
		t.Errorf("key field = %q, want the offending key", key)
	}
}

// TestSchemaRefusesADefaultOutsideItsOwnBounds is ADR 0031's trap, stated
// exactly: a schema whose default sits outside the bounds it also declares
// yields an invalid configuration on precisely the deployment where nobody set
// the key — and blames the operator for it. It is refused at construction.
func TestSchemaRefusesADefaultOutsideItsOwnBounds(t *testing.T) {
	t.Parallel()
	_, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{
		Defaults: []coreconfig.DeclaredValue{{Key: "database.max_conns", Value: 0}},
	})
	if err == nil {
		t.Fatal("NewSchema accepted max_conns default 0 against its own min=1")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigSchemaInvalid) {
		t.Errorf("code = %v, want CONFIG_SCHEMA_INVALID", err)
	}
	if key := fieldValue(t, err, "key"); key != "database.max_conns" {
		t.Errorf("key field = %q, want database.max_conns", key)
	}
	if clause := fieldValue(t, err, "clause"); !strings.Contains(clause, "min") {
		t.Errorf("clause = %q, want the rule that was violated", clause)
	}
}

// TestSchemaRefusesWhatCannotWork covers the remaining construction-time
// refusals. Each of them would otherwise produce a default that silently never
// applies, which is the quietest way a configuration can be wrong.
func TestSchemaRefusesWhatCannotWork(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		defaults []coreconfig.DeclaredValue
		wantKey  string
	}{
		{
			name:     "a key that names no field",
			defaults: []coreconfig.DeclaredValue{{Key: "database.max_conn", Value: 4}},
			wantKey:  "database.max_conn",
		},
		{
			name:     "a key outside the grammar",
			defaults: []coreconfig.DeclaredValue{{Key: "database..host", Value: "x"}},
			wantKey:  "database..host",
		},
		{
			name:     "an empty key",
			defaults: []coreconfig.DeclaredValue{{Key: "", Value: 1}},
			wantKey:  "",
		},
		{
			name:     "the same key twice",
			defaults: []coreconfig.DeclaredValue{{Key: "port", Value: 1}, {Key: "port", Value: 2}},
			wantKey:  "port",
		},
		{
			name:     "a key declared both as a value and as a table",
			defaults: []coreconfig.DeclaredValue{{Key: "database", Value: 1}, {Key: "database.host", Value: "x"}},
			wantKey:  "database.host",
		},
		{
			name:     "a default of the wrong type for its field",
			defaults: []coreconfig.DeclaredValue{{Key: "port", Value: "eighty"}},
			wantKey:  "",
		},
		{
			name:     "a value the round trip cannot carry",
			defaults: []coreconfig.DeclaredValue{{Key: "mode", Value: make(chan int)}},
			wantKey:  "mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{Defaults: tc.defaults})
			if err == nil {
				t.Fatalf("NewSchema accepted %s", tc.name)
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

// TestASchemaThatDeclaresNothingIsLegitimate is ADR 0031's accepting half. A
// configuration with nothing to check has nothing to contradict; refusing it
// would make it impossible to adopt a schema one key at a time.
func TestASchemaThatDeclaresNothingIsLegitimate(t *testing.T) {
	t.Parallel()
	type plain struct {
		Name string `json:"name"`
	}
	schema, err := cfg.NewSchemaValue[plain](cfg.SchemaSpec[plain]{})
	if err != nil {
		t.Fatalf("NewSchema on an untagged, undefaulted type: %v", err)
	}
	var conf plain
	if err := cfg.LoadSchema(&conf, schema); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if !schema.Check(conf).OK() {
		t.Error("an empty schema reported a violation")
	}
}

// TestTheTagsOfTheTypeStillApplyWithoutADefault pins that a schema declaring no
// default is not a schema declaring no rules: the `validate` tags of the type
// are the schema's own, and a second place to look for the same answer is what
// the compiled constraint avoids.
func TestTheTagsOfTheTypeStillApplyWithoutADefault(t *testing.T) {
	t.Parallel()
	schema, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	var conf srvSchemaConf
	if err := cfg.LoadSchema(&conf, schema); err == nil {
		t.Fatal("LoadSchema accepted an all-zero configuration against required/min tags")
	}
}

// TestCrossFieldRulesComposeWithTheTags shows where a rule a tag cannot express
// lives, and that it reports at the root — the position the grammar reserves
// for "no single key is at fault".
func TestCrossFieldRulesComposeWithTheTags(t *testing.T) {
	t.Parallel()
	prodNeedsRealHost := func(path string, value srvSchemaConf) corevalidation.ReportValue {
		if value.Mode != "prod" || value.Database.Host != "localhost" {
			return nil
		}
		return corevalidation.ReportValue{{
			Path:    path,
			Rule:    "prod_host",
			Message: "mode prod requires a database host other than the loopback default",
		}}
	}
	schema, err := cfg.NewSchemaValue[srvSchemaConf](cfg.SchemaSpec[srvSchemaConf]{
		Defaults: []coreconfig.DeclaredValue{
			{Key: "port", Value: 8080},
			{Key: "timeout", Value: 30},
			{Key: "mode", Value: "dev"},
			{Key: "database.host", Value: "localhost"},
			{Key: "database.max_conns", Value: 16},
			{Key: "database.password", Value: "placeholder-not-a-secret"},
		},
		Rule: prodNeedsRealHost,
	})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	var conf srvSchemaConf
	loadErr := cfg.LoadSchema(&conf, schema, mapSource{values: map[string]any{"mode": "prod"}})
	if loadErr == nil {
		t.Fatal("LoadSchema accepted prod on the loopback default")
	}
	if rule := fieldValue(t, loadErr, "rule"); rule != "prod_host" {
		t.Errorf("rule field = %q, want the cross-field rule name", rule)
	}
	//: the root path renders as the empty string, exactly as
	//: core/validation.ReportValue.Err does — one rendering of the grammar,
	//: not two.
	if keys := fieldValue(t, loadErr, "keys"); keys != corevalidation.RootPath {
		t.Errorf("keys field = %q, want the root path for a cross-field rule", keys)
	}
}

// validatorConf records whether its Validate method ran, so the ordering
// between the schema and the target's own self-check is observable.
type validatorConf struct {
	Port int `json:"port" validate:"min=1"`
	//: a pointer so the copy Load hands to Validate still writes to the test.
	//: unexported, so encoding/json never sees it and no tag is needed.
	ran *bool
}

// Validate implements core/config.Validator.
func (v validatorConf) Validate() error {
	//: record the visit; the test asserts on it.
	if v.ran != nil {
		*v.ran = true
	}
	return nil
}

// TestValidatorStillRunsAndTheSchemaRunsFirst is ADR 0039 applied: the
// published Validator port is neither widened nor bypassed. It keeps being
// asked — and it is asked AFTER the schema, because a hand-written cross-field
// assertion has no useful answer while the keys it reads are still invalid.
func TestValidatorStillRunsAndTheSchemaRunsFirst(t *testing.T) {
	t.Parallel()
	schema, err := cfg.NewSchemaValue[validatorConf](cfg.SchemaSpec[validatorConf]{
		Defaults: []coreconfig.DeclaredValue{{Key: "port", Value: 8080}},
	})
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	ran := false
	accepted := new(validatorConf)
	accepted.ran = &ran
	if err := cfg.LoadSchema(accepted, schema); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if !ran {
		t.Error("Validate was not called — the schema replaced the port instead of feeding it")
	}

	ran = false
	refused := new(validatorConf)
	refused.ran = &ran
	if err := cfg.LoadSchema(refused, schema, mapSource{values: map[string]any{"port": 0}}); err == nil {
		t.Fatal("LoadSchema accepted port 0 against min=1")
	}
	if ran {
		t.Error("Validate ran after the schema had already refused the configuration")
	}
}

// TestNilSchemaIsRefusedByName pins that an inert schema cannot be spelled: it
// would default nothing and check nothing while looking exactly like a working
// one, which is the accepting — and more dangerous — half of the ADR 0031 trap.
func TestNilSchemaIsRefusedByName(t *testing.T) {
	t.Parallel()
	var conf srvSchemaConf
	err := cfg.LoadSchema(&conf, nil)
	if err == nil {
		t.Fatal("LoadSchema accepted a nil schema")
	}
	if !errs.HasCode(err, coreconfig.CodeConfigSchemaInvalid) {
		t.Errorf("code = %v, want CONFIG_SCHEMA_INVALID", err)
	}
}

// TestSourceYieldsAFreshCopy pins the Source port's concurrency contract: a
// caller that mutates the layer it was handed cannot change what the next load
// sees.
func TestSourceYieldsAFreshCopy(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	layer, err := schema.Source().Load()
	if err != nil {
		t.Fatalf("Source().Load(): %v", err)
	}
	//: reach into the nested table and break it.
	nested, ok := layer["database"].(map[string]any)
	if !ok {
		t.Fatalf("database layer = %T, want a nested table", layer["database"])
	}
	nested["max_conns"] = 999
	delete(layer, "port")

	var conf srvSchemaConf
	if err := cfg.LoadSchema(&conf, schema); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if conf.Port != 8080 || conf.Database.MaxConns != 16 {
		t.Errorf("mutating a returned layer changed the schema: Port=%d MaxConns=%d", conf.Port, conf.Database.MaxConns)
	}
}

// routeSchemaConf is one element of an array of tables.
type routeSchemaConf struct {
	Path string   `json:"path"`
	Tags []string `json:"tags"`
}

// arraySchemaConf is a target whose defaults are arrays — of scalars, and of
// tables holding arrays — the shapes a decoded layer holds by reference.
type arraySchemaConf struct {
	Hosts  []string          `json:"hosts"`
	Routes []routeSchemaConf `json:"routes"`
}

// arraySchema builds a schema that defaults both array shapes.
func arraySchema(t *testing.T) *cfg.SchemaValue[arraySchemaConf] {
	t.Helper()
	schema, err := cfg.NewSchemaValue[arraySchemaConf](cfg.SchemaSpec[arraySchemaConf]{
		Defaults: []coreconfig.DeclaredValue{
			{Key: "hosts", Value: []string{"a", "b"}},
			{Key: "routes", Value: []routeSchemaConf{{Path: "/", Tags: []string{"public"}}}},
		},
	})
	if err != nil {
		t.Fatalf("NewSchemaValue: %v", err)
	}
	return schema
}

// TestSourceYieldsAFreshCopyOfEveryArray is TestSourceYieldsAFreshCopy for the
// shapes it did not reach: an array, a table inside an array, and an array
// inside that table. ADR 0061 promises a fresh copy on every Load; arrays used
// to be handed back by reference, so a caller that edited one — printing the
// effective defaults and "fixing" an entry, say — rewrote the compiled schema,
// and every later load decoded the edit as if the author had written it.
//
// MUTATION (2026-09-11): cloneNested put back to copying maps only (HEAD's
// merge.go). Observed: `the next Source().Load sees the caller's edit: hosts =
// []interface {}{"rewritten", "b"}` and `LoadSchema decoded the caller's edit:
// Hosts=[rewritten b] Routes=[{Path:/rewritten Tags:[rewritten]}]` — all three
// depths of the edit reached the schema. Restored; SHA-256 of merge.go
// identical to the pre-mutation file.
func TestSourceYieldsAFreshCopyOfEveryArray(t *testing.T) {
	t.Parallel()
	schema := arraySchema(t)
	layer, err := schema.Source().Load()
	if err != nil {
		t.Fatalf("Source().Load(): %v", err)
	}
	hosts, isArray := layer["hosts"].([]any)
	routes, isRoutes := layer["routes"].([]any)
	if !isArray || !isRoutes || len(hosts) != 2 || len(routes) != 1 {
		t.Fatalf("layer = %#v, want two arrays", layer)
	}
	route, isTable := routes[0].(map[string]any)
	tags, hasTags := route["tags"].([]any)
	if !isTable || !hasTags {
		t.Fatalf("routes[0] = %#v, want a table carrying an array", routes[0])
	}
	//: edit all three depths of what the caller was handed.
	hosts[0], route["path"], tags[0] = "rewritten", "/rewritten", "rewritten"

	again, err := schema.Source().Load()
	if err != nil {
		t.Fatalf("Source().Load(): %v", err)
	}
	if next, _ := again["hosts"].([]any); len(next) == 0 || next[0] != "a" {
		t.Errorf("the next Source().Load sees the caller's edit: hosts = %#v", next)
	}
	var conf arraySchemaConf
	if err := cfg.LoadSchema(&conf, schema); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	pristine := len(conf.Hosts) == 2 && conf.Hosts[0] == "a" && len(conf.Routes) == 1 &&
		conf.Routes[0].Path == "/" && len(conf.Routes[0].Tags) == 1 && conf.Routes[0].Tags[0] == "public"
	if !pristine {
		t.Errorf("LoadSchema decoded the caller's edit: Hosts=%v Routes=%+v", conf.Hosts, conf.Routes)
	}
}

// TestAnArrayReplacesItsDefault pins what copying arrays must not turn into:
// a source that supplies an array REPLACES the default one, whole. Merging the
// two element by element would decode a list no layer wrote.
//
// MUTATION (2026-09-11): deepMerge made to append a source array to the one
// already under the key. Observed: `Hosts = [a b c], want [c] — an array
// replaces its default, it is not merged into it`, and in Test_deepMerge's `an
// array replaces an array, whole`: `key "xs" = []interface {}{1, 2, 3}, want
// []interface {}{3}` and the same for the nested `ys`. Restored; SHA-256 of
// merge.go identical to the pre-mutation file.
func TestAnArrayReplacesItsDefault(t *testing.T) {
	t.Parallel()
	var conf arraySchemaConf
	override := mapSource{values: map[string]any{"hosts": []any{"c"}}}
	if err := cfg.LoadSchema(&conf, arraySchema(t), override); err != nil {
		t.Fatalf("LoadSchema: %v", err)
	}
	if len(conf.Hosts) != 1 || conf.Hosts[0] != "c" {
		t.Errorf("Hosts = %v, want [c] — an array replaces its default, it is not merged into it", conf.Hosts)
	}
	//: a key the source did not name keeps its default untouched.
	if len(conf.Routes) != 1 || conf.Routes[0].Path != "/" {
		t.Errorf("Routes = %+v, want the default", conf.Routes)
	}
}

// TestCheckCarriesTheMessagesTheErrorCannot pins the division of labour: the
// error is the interop shape (count, first rule, keys) and the report is the
// report. A caller who wants to render a per-key message asks Check.
func TestCheckCarriesTheMessagesTheErrorCannot(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	conf := srvSchemaConf{Port: 0, Timeout: 30, Mode: "prod"}
	conf.Database = dbSchemaConf{Host: "h", MaxConns: 1, Password: "long-enough-1"}

	report := schema.Check(conf)
	if report.OK() {
		t.Fatal("Check accepted port 0 against min=1")
	}
	first, ok := report.First()
	if !ok {
		t.Fatal("a non-OK report has no first violation")
	}
	if first.Path != "port" {
		t.Errorf("first.Path = %q, want %q", first.Path, "port")
	}
	if first.Message == "" {
		t.Error("first.Message is empty — the report carries what the error cannot")
	}
	if !errs.HasCode(report.Err(), corevalidation.CodeValidationFailed) {
		t.Errorf("report.Err() = %v, want VALIDATION_FAILED", report.Err())
	}
}

// TestTheEngineIsConsumableFromAValidator is the five-line bridge ADR 0046
// promised, now with the defaults on the same schema: a struct keeps its own
// Validate method and implements it by asking the engine.
func TestTheEngineIsConsumableFromAValidator(t *testing.T) {
	t.Parallel()
	schema := baseSchema(t)
	conf := srvSchemaConf{Port: 70000, Timeout: 30, Mode: "prod"}
	conf.Database = dbSchemaConf{Host: "h", MaxConns: 1, Password: "long-enough-1"}

	//: exactly what a Validator method body would be.
	err := schema.Check(conf).Err()
	if err == nil {
		t.Fatal("Check().Err() returned nil for a port above max=65535")
	}
	//: and a passing check returns a genuine nil, not a typed one.
	conf.Port = 8080
	if passing := schema.Check(conf).Err(); passing != nil {
		t.Errorf("Check().Err() on a valid config = %v, want a genuine nil", passing)
	}
}

// TestARefusedTagKeepsTheValidationDomainsDiagnosis pins the deliberate
// non-relabelling: the tag compiler's fields already name the field, the rule
// and the clause, and rewriting the code to a config one would trade that
// diagnosis for a code the caller has to look up anyway.
func TestARefusedTagKeepsTheValidationDomainsDiagnosis(t *testing.T) {
	t.Parallel()
	type tagged struct {
		Name string `json:"name" validate:"pattern=^a"`
	}
	_, err := cfg.NewSchemaValue[tagged](cfg.SchemaSpec[tagged]{})
	if err == nil {
		t.Fatal("NewSchema accepted a tag-borne regexp")
	}
	if !errs.HasCode(err, svcvalidation.CodeInvalidRule) {
		t.Errorf("code = %v, want the validation domain's INVALID_RULE", err)
	}
}

// fieldValue returns the string rendering of one field on err, or "" when the
// field is absent.
func fieldValue(tb testing.TB, err error, key string) string {
	tb.Helper()
	for _, field := range errs.FieldsOf(err) {
		if field.Key() == key {
			return field.StringValue()
		}
	}
	return ""
}

// assertNoProbe fails when text repeats the secret probe anywhere.
func assertNoProbe(tb testing.TB, where, text string) {
	tb.Helper()
	if strings.Contains(text, secretProbe) {
		tb.Errorf("%s leaked the rejected value: %q", where, text)
	}
}
