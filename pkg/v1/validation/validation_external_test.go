package validation_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/config"
	"github.com/kitsunium/sdk/pkg/v1/validation"
)

// serverConf is the shape the config bridge is demonstrated on. Its rules live
// in tags; its Validate method runs them. Nothing else about it is special —
// which is the point.
type serverConf struct {
	Name string `json:"name" validate:"required,maxlen=32"`
	Port int    `json:"port" validate:"min=1,max=65535"`
}

// Validate makes serverConf a config.Validator by running the validation
// engine. This is the articulation ADR 0046 exists to make explicit: the
// config domain owns the CONTRACT ("is this decoded struct coherent?"), this
// domain owns the ENGINE that answers it, and neither replaces the other.
func (c serverConf) Validate() error {
	//: the plan is compiled once per type and cached, so looking it up here —
	//: on every Load — is a map read, not a compilation.
	rules, err := validation.Struct[serverConf](validation.StructConfig{})
	//: a refused tag is a source defect and must surface as one.
	if err != nil {
		//: propagate the typed refusal.
		return err
	}
	//: Check converts the report to the SDK error model, nil when it passes.
	return validation.Check(c, rules)
}

// TestServerConfSatisfiesTheConfigValidator is the compile-time half of the
// claim: the engine's output fits the existing contract without either side
// being changed.
func TestServerConfSatisfiesTheConfigValidator(t *testing.T) {
	t.Parallel()
	validator := config.Validator(serverConf{})
	if err := validator.Validate(); err == nil {
		t.Fatal("an empty config must fail its own validation")
	}
}

// TestConfigLoadSurfacesAValidationFailure runs the whole path: env source →
// decode → Validate → typed error. A caller sees config's own
// CONFIG_VALIDATION_FAILED, because that is the contract they called.
func TestConfigLoadSurfacesAValidationFailure(t *testing.T) {
	t.Setenv("APP_NAME", "gateway")
	t.Setenv("APP_PORT", "99999")
	var conf serverConf
	err := config.Load(&conf, config.EnvSource("APP"))
	if err == nil {
		t.Fatal("a port above 65535 must abort the load")
	}
	if !errors.Is(err, config.ValidationFailed) {
		t.Errorf("err = %v, want CONFIG_VALIDATION_FAILED", err)
	}
}

// TestConfigLoadAcceptsAValidConfiguration is the other half: the bridge must
// not turn every load into a failure.
func TestConfigLoadAcceptsAValidConfiguration(t *testing.T) {
	t.Setenv("APP_NAME", "gateway")
	t.Setenv("APP_PORT", "8080")
	var conf serverConf
	if err := config.Load(&conf, config.EnvSource("APP")); err != nil {
		t.Fatalf("a valid configuration must load, got %v", err)
	}
	if conf.Port != 8080 || conf.Name != "gateway" {
		t.Errorf("conf = %+v, want the decoded values", conf)
	}
}

// TestFacadeAliasesTheDomain covers the surface the facade publishes: the
// aliases are the same types, and the delegations reach the engine.
func TestFacadeAliasesTheDomain(t *testing.T) {
	t.Parallel()
	rules := validation.All(
		validation.Required[string](),
		validation.Must(validation.Length(3, validation.Unbounded)),
	)
	report := rules(validation.RootPath, "")
	if len(report) != 2 {
		t.Fatalf("both rules must report, got %v", report)
	}
	first := report[0]
	if first.Code != validation.CodeRequired {
		t.Errorf("code = %v, want CodeRequired", first.Code)
	}
	if err := report.Err(); err == nil || !errors.Is(err, validation.Failed) {
		t.Errorf("Err = %v, want VALIDATION_FAILED", err)
	}
	if report := rules(validation.RootPath, "abcd"); !report.OK() {
		t.Errorf("a valid value must pass, got %v", report)
	}
}

// TestFacadePathHelpers pins the grammar the facade re-exports.
func TestFacadePathHelpers(t *testing.T) {
	t.Parallel()
	got := validation.JoinField(validation.JoinIndex(
		validation.JoinField("user", "addresses"), 2), "zip")
	if got != "user.addresses[2].zip" {
		t.Errorf("path = %q, want %q", got, "user.addresses[2].zip")
	}
}

// TestFacadeRefusalsStayTyped proves the ADR 0031 refusals survive the facade.
func TestFacadeRefusalsStayTyped(t *testing.T) {
	t.Parallel()
	if _, err := validation.Between(10, 1); err == nil {
		t.Error("an inverted interval must be refused through the facade too")
	}
	if _, err := validation.OneOf[string](); err == nil {
		t.Error("an empty allowed set must be refused")
	}
	_, err := validation.Struct[serverConf](validation.StructConfig{})
	if err != nil {
		t.Errorf("a legal type must compile, got %v", err)
	}
	_, err = validation.Struct[string](validation.StructConfig{})
	if err == nil || !strings.Contains(err.Error(), "struct") {
		t.Errorf("a non-struct target must be refused by name, got %v", err)
	}
}
