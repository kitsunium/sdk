package studiokit

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// The tool's variables (ADR 0180 §3, step 1): what kit dev sets for the
// process it launches — which variables it set itself, and what it built —
// and what turns the Studio on or off, to remote clients and with the static
// analysis. kit's core reads none of them: a program that does not import
// framework/kit/studio is described and configured as one none of them is
// set for.

// Environment kit dev gives the process it launches, describing the build.
const (
	envDevBuiltAt     = "KIT_DEV_BUILT_AT"
	envDevBuildMs     = "KIT_DEV_BUILD_MS"
	envDevBuilds      = "KIT_DEV_REBUILDS"
	envDevChanged     = "KIT_DEV_CHANGED"
	envDevMore        = "KIT_DEV_CHANGED_MORE"
	maxDevChanged int = 20
)

// studioEnv reads what the tool's variables ask of the Studio.
func studioEnv(getenv func(string) string) plug.StudioEnvValue {
	studio := getenv(model.VarStudio)
	if studio != "on" && studio != "off" {
		studio = ""
	}
	return plug.StudioEnvValue{
		Studio:     studio,
		Remote:     getenv(model.VarStudioRemote) == "on",
		NoAnalysis: getenv(model.VarAnalyze) == "off",
	}
}

// studioSettings are the tool's variables as the settings of a run show
// them: the Studio, its remote clients and the static analysis, each with
// its value and where it came from.
func studioSettings(getenv func(string) string, s plug.StudioStateValue) []model.Setting {
	return []model.Setting{
		{Name: model.VarStudio, Value: s.Studio, From: settingFrom(getenv, s.StudioOption != nil, model.VarStudio), Option: "kit.Studio"},
		{Name: model.VarStudioRemote, Value: s.Remote, From: settingFrom(getenv, false, model.VarStudioRemote)},
		{Name: model.VarAnalyze, Value: s.Analyze, From: settingFrom(getenv, s.AnalyzeOption != nil, model.VarAnalyze), Option: "kit.Analyze"},
	}
}

// settingFrom is where the setting read from name comes from: its option
// when the code gives one, the environment when name is set, its default
// otherwise. kit dev never sets one of the tool's variables as its own
// (kit labels only those it lets kit dev set).
func settingFrom(getenv func(string) string, option bool, name string) string {
	switch {
	case option:
		return model.SettingOption
	case getenv(name) != "":
		return model.SettingEnv
	default:
		return model.SettingDefault
	}
}

// devForced are the variables kit dev says it set itself for this launch
// (model.VarDevForced), as it names them: a label and nothing else.
func devForced(getenv func(string) string) []string {
	var out []string
	for name := range strings.SplitSeq(getenv(model.VarDevForced), ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// devBuildOf reads what kit dev said about the build it launched, or nil
// when the process was not started by kit dev.
func devBuildOf(getenv func(string) string) *model.DevBuild {
	at, err := time.Parse(time.RFC3339Nano, getenv(envDevBuiltAt))
	if err != nil {
		return nil
	}
	d := &model.DevBuild{BuiltAt: new(at.UTC()), Changed: changedFiles(getenv(envDevChanged))}
	if ms, err := strconv.ParseFloat(getenv(envDevBuildMs), 64); err == nil && ms >= 0 {
		d.BuildMs = round2(ms)
	}
	d.Rebuilds, d.More = positiveInt(getenv(envDevBuilds)), positiveInt(getenv(envDevMore))
	return d
}

// changedFiles are the files a dev build says changed, one a line: local
// paths only, at most maxDevChanged.
func changedFiles(raw string) []string {
	var out []string
	for f := range strings.SplitSeq(raw, "\n") {
		if f = strings.TrimSpace(f); f != "" && filepath.IsLocal(filepath.FromSlash(f)) && len(out) < maxDevChanged {
			out = append(out, f)
		}
	}
	return out
}

// positiveInt is raw as a positive integer, 0 when it is not one.
func positiveInt(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return 0
}
