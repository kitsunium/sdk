package kit_test

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/model"
)

// The start is recorded as it ran: the Studio draws it as the daemon's boot
// sequence, and shows the configuration it read.
func TestTheStartIsRecordedStepByStep(t *testing.T) {
	t.Setenv("KIT_TRUST_PROXY", "on")
	app := start(t)
	rt := app.Graph().Runtime
	if rt == nil {
		t.Fatal("a running app describes its runtime")
	}
	var names []string
	for _, s := range rt.Boot {
		names = append(names, s.Name)
		if s.Begun.IsZero() || s.TookMs < 0 || s.Error != "" {
			t.Errorf("step %s: %+v", s.Name, s)
		}
	}
	want := []string{
		model.BootConfig, model.BootDeclarations, model.BootMount, model.BootRoutes,
		model.BootData, model.BootHandler, model.BootComponents, model.BootServing,
	}
	if !slices.Equal(names, want) {
		t.Fatalf("boot steps %v, want %v", names, want)
	}
	step := func(name string) model.BootStep {
		for _, s := range rt.Boot {
			if s.Name == name {
				return s
			}
		}
		return model.BootStep{}
	}
	if s := step(model.BootMount); s.Count != 3 {
		t.Errorf("mount counts the services: %d", s.Count)
	}
	if s := step(model.BootRoutes); s.Count == 0 {
		t.Error("routes counts the routes")
	}
	if s := step(model.BootData); s.Value != "memory" {
		t.Errorf("an in-memory app says so: %q", s.Value)
	}
	if s := step(model.BootComponents); s.Count != len(rt.Components) {
		t.Errorf("components: %d, want %d", s.Count, len(rt.Components))
	}
	if s := step(model.BootConfig); s.Count != len(rt.Config) {
		t.Errorf("config counts the settings: %d, want %d", s.Count, len(rt.Config))
	}
	setting := func(name string) model.Setting {
		for _, s := range rt.Config {
			if s.Name == name {
				return s
			}
		}
		t.Fatalf("no setting %s", name)
		return model.Setting{}
	}
	if s := setting("KIT_ENV"); s.Value != "dev" || s.From != model.SettingOption {
		t.Errorf("KIT_ENV: %+v", s)
	}
	if s := setting("KIT_DATA_DIR"); s.Value != "memory" || s.From != model.SettingOption {
		t.Errorf("KIT_DATA_DIR: %+v", s)
	}
	if s := setting("KIT_TRUST_PROXY"); s.Value != "on" || s.From != model.SettingEnv {
		t.Errorf("KIT_TRUST_PROXY: %+v", s)
	}
	if s := setting("KIT_SMTP_URL"); !s.Secret || s.Value != "" {
		t.Errorf("KIT_SMTP_URL is a secret: %+v", s)
	}
}
