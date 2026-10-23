package app

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// The framework's own export code — the export's settings and what they
// refuse, its component, its readiness, its stop's budget, its secret as the
// configuration shows it (otlp.go, otlp_run.go) — names no SDK type of the
// export's and stays in the core; a program reaches it only through
// otlpCode, which framework/app/otlp sets as it is imported (EnableOTLP), as
// framework/app/store sets the stores' (EnableStores). A program that does
// not import it links none of that code. Each call site falls back to what
// an app without an export does: no setting, no problem, no component, no
// budget, nothing to show. The tracer's sink and the record of a unit of
// work stay reachable: each is one atomic load without an export.

// otlpHooks are the entries of the framework's own export code that the
// rest of the core calls, each a function of the app.
type otlpHooks struct {
	// add registers the export's component and its readiness check.
	add func(a *App) error
	// settings are the export's settings but for those a product's setting
	// names; problems what refuses the export's start.
	settings func(a *App, byName map[string]settingDecl) []settingDecl
	problems func(a *App) []model.Diagnostic
	// stopTakes is the most the export's stop takes.
	stopTakes func(a *App) time.Duration
	// headerSettings is the export's secret as the configuration shows it.
	headerSettings func(a *App, ctx context.Context) []model.Setting
	// tables are the export's tables on a database (issue #537).
	tables func(a *App, d *database) []frameworkTable
}

// otlpCode is the framework's own export code, nil until framework/app/otlp
// is imported.
var otlpCode atomic.Pointer[otlpHooks]

// EnableOTLP makes the framework's own export code reachable: the settings,
// the component, the readiness and the stop of the OTLP export a product
// declares. framework/app/otlp calls it as it is imported; a product never
// does.
func EnableOTLP() {
	otlpCode.Store(&otlpHooks{
		add:            (*App).addOTLP,
		settings:       (*App).otlpSettings,
		problems:       (*App).otlpProblems,
		stopTakes:      (*App).otlpStopBudget,
		headerSettings: (*App).otlpHeaderSettings,
		tables:         (*App).otlpTables,
	})
}

// otlpCodeTables are the export's tables on d, none without
// framework/app/otlp.
func (a *App) otlpCodeTables(d *database) []frameworkTable {
	if h := otlpCode.Load(); h != nil {
		return h.tables(a, d)
	}
	return nil
}

// addOTLPComponent adds the export's component, when the program imports
// framework/app/otlp; an export declared without it is refused before
// (subsystem.go).
func (a *App) addOTLPComponent() error {
	if h := otlpCode.Load(); h != nil {
		return h.add(a)
	}
	return nil
}

// otlpCodeSettings are the export's settings, none without
// framework/app/otlp.
func (a *App) otlpCodeSettings(byName map[string]settingDecl) []settingDecl {
	if h := otlpCode.Load(); h != nil {
		return h.settings(a, byName)
	}
	return nil
}

// otlpCodeProblems are what refuses the export's start, none without
// framework/app/otlp — whose absence is a problem of its own.
func (a *App) otlpCodeProblems() []model.Diagnostic {
	if h := otlpCode.Load(); h != nil {
		return h.problems(a)
	}
	return nil
}

// otlpStopTakes is the most the export's stop takes, nothing without
// framework/app/otlp.
func (a *App) otlpStopTakes() time.Duration {
	if h := otlpCode.Load(); h != nil {
		return h.stopTakes(a)
	}
	return 0
}

// otlpCodeHeaderSettings is the export's secret as the configuration shows
// it, nothing without framework/app/otlp.
func (a *App) otlpCodeHeaderSettings(ctx context.Context) []model.Setting {
	if h := otlpCode.Load(); h != nil {
		return h.headerSettings(a, ctx)
	}
	return nil
}
