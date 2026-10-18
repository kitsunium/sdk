package studioengine

import (
	"slices"
	"testing"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// env is a fixed environment.
func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestDevBuildOfTheEnvironment(t *testing.T) {
	vars := map[string]string{
		envDevBuiltAt: "2026-09-24T10:00:00.5Z",
		envDevBuildMs: "1850",
		envDevBuilds:  "7",
		envDevChanged: "identity/api.go\n../../etc/passwd\n/abs/path.go\nnotify/locales/fr.json",
		envDevMore:    "5",
	}
	d := devBuildOf(env(vars))
	if d == nil || d.BuildMs != 1850 || d.Rebuilds != 7 || d.More != 5 || d.BuiltAt == nil {
		t.Fatalf("dev build %+v", d)
	}
	if len(d.Changed) != 2 || d.Changed[0] != "identity/api.go" || d.Changed[1] != "notify/locales/fr.json" {
		t.Errorf("only module-relative paths are kept: %v", d.Changed)
	}
	if devBuildOf(env(nil)) != nil {
		t.Error("a process kit dev did not start has no dev build")
	}
}

// The tool's variables say what they ask of the Studio; none set is the
// zero value, which kit's core takes without the Studio.
func TestTheToolsVariablesAskTheStudio(t *testing.T) {
	for _, c := range []struct {
		vars map[string]string
		want plug.StudioEnvValue
	}{
		{nil, plug.StudioEnvValue{}},
		{map[string]string{model.VarStudio: "on", model.VarStudioRemote: "on"}, plug.StudioEnvValue{Studio: "on", Remote: true}},
		{map[string]string{model.VarStudio: "off", model.VarAnalyze: "off"}, plug.StudioEnvValue{Studio: "off", NoAnalysis: true}},
		{map[string]string{model.VarStudio: "yes", model.VarStudioRemote: "1", model.VarAnalyze: "no"}, plug.StudioEnvValue{}},
	} {
		if got := studioEnv(env(c.vars)); got != c.want {
			t.Errorf("%v: %+v, want %+v", c.vars, got, c.want)
		}
	}
	forced := devForced(env(map[string]string{model.VarDevForced: " KIT_ENV , ,KIT_ADDR,"}))
	if !slices.Equal(forced, []string{model.VarEnv, model.VarAddr}) {
		t.Errorf("forced: %q", forced)
	}
}

// The settings of the tool's variables say each value and where it came
// from: the product's code, the environment, or kit's default.
func TestTheToolsSettingsSayWhereEachComesFrom(t *testing.T) {
	set := studioSettings(env(map[string]string{model.VarStudioRemote: "on"}), plug.StudioStateValue{
		Studio: "on", Remote: "on", Analyze: "off", AnalyzeOption: new(false),
	})
	want := []model.Setting{
		{Name: model.VarStudio, Value: "on", From: model.SettingDefault, Option: "app.Studio"},
		{Name: model.VarStudioRemote, Value: "on", From: model.SettingEnv},
		{Name: model.VarAnalyze, Value: "off", From: model.SettingOption, Option: "app.Analyze"},
	}
	if !slices.Equal(set, want) {
		t.Errorf("settings %+v, want %+v", set, want)
	}
}

// Enable plugs the Studio in: kit then reads the tool's variables and
// mounts the Studio's routes through plug.Studio.
func TestEnablePlugsTheStudioIn(t *testing.T) {
	saved, words := plug.Studio.Load(), plug.Words.Load()
	t.Cleanup(func() {
		plug.Studio.Store(saved)
		plug.Words.Store(words)
	})
	plug.Studio.Store(nil)
	Enable()
	d := plug.Studio.Load()
	if d == nil || d.Env == nil || d.Settings == nil || d.Forced == nil || d.DevBuild == nil || d.Mount == nil {
		t.Fatalf("Enable left the Studio unplugged: %+v", d)
	}
}
