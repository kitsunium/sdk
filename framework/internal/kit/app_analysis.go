// Package kit — the static analysis a product can be given: the platform's
// analyzer, run in the background in dev (ADR 0147 §1).
package kit

import (
	"context"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// AnalyzeFunc reads the product's source — the Go module rooted at dir, and
// the packages that declare the modules it mounts — into a graph the runtime
// merges into its own: the edges found in handler bodies, each function's
// steps. The framework links no analyzer (ADR 0147 §1): the platform's kit
// tool gives one, in dev, through [Analyzer].
type AnalyzeFunc func(ctx context.Context, dir string, modules []string) (*model.Graph, error)

// Analyzer runs fn in the background once the product serves, in dev and when
// KIT_ANALYZE allows it. Without one, the graph is the runtime's alone.
func Analyzer(fn AnalyzeFunc) AppConfigurer {
	return appOption(func(o *appOptions) { o.analyzer = fn })
}

// startAnalysis launches the static analysis of the product's source in the
// background: the product serves at once, and the diagram gains the edges
// found in the code a moment later.
//
// Goroutine lifecycle: one goroutine in a.wg, which ends when the analyzer
// returns — it is given the app's base context, which the stop cancels — and
// the stop waits for a.wg.
func (a *App) startAnalysis(_ context.Context) error {
	a.mu.Lock()
	a.analysis = &model.Analysis{Status: "running"}
	a.mu.Unlock()
	a.wg.Go(func() {
		a.runAnalysis(a.baseCtx)
		a.graphChanged()
	})
	return nil
}

// runAnalysis analyzes the module the app was declared in — and the Go
// modules of the modules it mounts — and keeps the result for Graph to
// merge. What the analyzer's error says reaches the log only; the graph
// carries a fixed sentence.
func (a *App) runAnalysis(ctx context.Context) {
	started := time.Now()
	g, err := a.opts.analyzer(ctx, a.root, a.modulePackages())
	status := &model.Analysis{Status: "ok", At: new(time.Now().UTC()), TookMs: round2(float64(time.Since(started)) / float64(time.Millisecond))}
	switch {
	case err != nil:
		status.Status, status.Error = "failed", "the static analysis failed: the log says why"
		g = nil
	case g != nil && g.Analysis != nil:
		status.Packages = g.Analysis.Packages
	}
	if err != nil && ctx.Err() == nil {
		logger.Warn(ctx, a.log, "static analysis", logger.String("status", status.Status), logger.String("error", err.Error()))
	}
	a.mu.Lock()
	a.static, a.analysis = g, status
	a.mu.Unlock()
}

// modulePackages are the packages that declare the modules the app mounts,
// which the analysis loads and counts mounted; nil without one, so the
// analysis reads the product alone.
func (a *App) modulePackages() []string {
	var out []string
	for _, mm := range a.modules {
		if pkg := mm.module.decl.pkg(); pkg != "" {
			out = append(out, pkg)
		}
	}
	return out
}
