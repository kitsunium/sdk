package plug

import (
	"net/http"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
)

// The Studio's contract (ADR 0180 §4, step 1): what kit's core asks of the
// Studio subsystem, and what it hands it. framework/app/studio sets the hook
// and framework/internal/app/studioengine implements it: the tool's variables —
// what kit dev sets for a launch, whether the Studio is served, to whom, and
// with the static analysis — are read there, and the Studio's routes, their
// guard and their handlers are mounted there, reading the running app
// through a StudioHost the core gives. The core reads none of the tool's
// variables and holds none of the Studio's routes: a program that does not
// import framework/app/studio links neither. The values are here, the host
// in studio_interface.go.

// Studio is the Studio subsystem: the tool's variables and the Studio's
// routes; nil until framework/app/studio is imported, and kit then reads
// none of the tool's variables and serves no route of the Studio.
var Studio atomic.Pointer[StudioDriverConfig]

// StudioDriverConfig is what framework/app/studio plugs into kit: how the
// tool's variables are read, and how the Studio's routes mount.
type StudioDriverConfig struct {
	// Env reads what the tool's variables ask of the Studio in this run.
	Env func(getenv func(string) string) StudioEnvValue
	// Settings are the tool's variables as the settings of a run show them,
	// in order, each with its value as the run resolved it and where it
	// came from: the product's code, the environment, or kit's default.
	Settings func(getenv func(string) string, state StudioStateValue) []model.Setting
	// Forced are the variables kit dev says it set itself for this launch,
	// as it names them; kit labels only those it lets kit dev set.
	Forced func(getenv func(string) string) []string
	// DevBuild is what kit dev said about the build it launched, nil when
	// the process was not started by kit dev.
	DevBuild func(getenv func(string) string) *model.DevBuild
	// Mount registers the Studio's routes on mux, each behind the Studio's
	// guard, reading the running app through host.
	Mount func(mux *http.ServeMux, host StudioHost)
}

// StudioEnvValue is what the tool's variables ask of the Studio: their
// zero value is a run in which none is set.
type StudioEnvValue struct {
	// Studio is what the environment says of the Studio: on — which
	// production refuses —, off, or empty when it says neither.
	Studio string
	// Remote: the Studio answers a client that is not on this machine.
	Remote bool
	// NoAnalysis: the environment turns the static analysis off.
	NoAnalysis bool
}

// StudioStateValue is what a run resolved of the Studio, and what the
// product's code chose of it: what the settings show of the tool's
// variables.
type StudioStateValue struct {
	// StudioOption and AnalyzeOption are the product's code's choice of the
	// Studio and of the static analysis, which wins over the environment;
	// nil when the code chose nothing.
	StudioOption, AnalyzeOption *bool
	// Studio, Remote and Analyze are the Studio's switches, as resolved and
	// as the settings spell them: on or off.
	Studio, Remote, Analyze string
}
