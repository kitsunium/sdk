// Package main — the rule table.
//
// Each rule states an SDK decision a consumer can violate silently, and each
// one is deliberately narrow. A rule that fires on legitimate code teaches
// people to ignore the tool, which costs more than the rule was worth: the
// SDK's own .golangci.yml drops misspell for exactly that reason.
package main

import "go/ast"

// slogPath is the stdlib structured-logging package (ADR 0032).
const slogPath string = "log/slog"

// logPath is the stdlib legacy logging package.
const logPath string = "log"

// osPath is the stdlib os package, source of Stdout (ADR 0030).
const osPath string = "os"

// fmtPath is the stdlib fmt package, source of Errorf (SDK rule 2).
const fmtPath string = "fmt"

// errorsPath is the stdlib errors package, source of New (SDK rule 2).
const errorsPath string = "errors"

// loggerPath is the SDK's public logger facade.
const loggerPath string = "github.com/kitsunium/sdk/pkg/v1/logger"

// bridgePath is the sanctioned slog adapter (ADR 0032).
const bridgePath string = "github.com/kitsunium/sdk/pkg/v1/logger/slogbridge"

// bridgeConstructorPath aliases bridgePath for the scanner, which resolves
// bridge-bound identifiers before any rule runs.
const bridgeConstructorPath string = bridgePath

// LevelInvariant marks a rule whose violation is a correctness defect: records
// silently dropped, a protocol stream corrupted, a binary misreporting itself.
const LevelInvariant string = "invariant"

// LevelConvention marks a rule the SDK offers rather than imposes. ADR 0019
// makes the error model available to downstreams; it does not oblige them. A
// team adopting the SDK incrementally runs -level=invariant first and turns
// conventions on when it is ready, instead of switching the whole tool off.
const LevelConvention string = "convention"

var (
	// allRules is the full rule table, ordered by ID.
	allRules = []ruleEntity{
		{
			ID:     "SDK001",
			Level:  LevelInvariant,
			Title:  "no second logging pipeline beside the SDK logger",
			Source: "ADR 0032",
			Check:  checkSlogPipeline,
		},
		{
			ID:     "SDK002",
			Level:  LevelConvention,
			Title:  "errors carry a typed code, not a formatted string",
			Source: "SDK rule 2 / ADR 0019",
			Check:  checkUntypedErrors,
		},
		{
			ID:     "SDK003",
			Level:  LevelInvariant,
			Title:  "stdout is a protocol channel, never a log destination",
			Source: "ADR 0030",
			Check:  checkStdoutDestination,
		},
		{
			ID:     "SDK004",
			Level:  LevelInvariant,
			Title:  "logger.Version is stamped at link time, not assigned",
			Source: "pkg/v1/logger/CLAUDE.md",
			Check:  checkVersionAssignment,
		},
		{
			ID:     "SDK005",
			Level:  LevelConvention,
			Title:  "no second logging pipeline via the legacy log package",
			Source: "ADR 0032",
			Check:  checkLegacyLog,
		},
	}

	// The distinction is the whole rule. Banning the log/slog import outright would
	// be simpler and wrong: a consumer of slogbridge imports it to type a
	// *slog.Logger field or to pass slog.String attrs to a foreign API. Only the
	// constructors and the process-wide default create a second destination.
	slogPipelineCalls = []pipelineCall{
		{"New", "slog.New builds a logger the SDK pipeline does not own"},
		{"NewTextHandler", "slog.NewTextHandler builds a second encoder and destination"},
		{"NewJSONHandler", "slog.NewJSONHandler builds a second encoder and destination"},
		{"SetDefault", "slog.SetDefault redirects the process-wide logger away from the SDK"},
		{"Default", "slog.Default reaches for a logger the SDK never configured"},
	}

	// untypedErrorCalls are the stdlib error constructors the SDK replaced.
	untypedErrorCalls = map[string]string{
		fmtPath:    "Errorf",
		errorsPath: "New",
	}

	// logDestinations are the constructors whose io.Writer argument becomes a log
	// destination. os.Stdout reaching any of them is the ADR 0030 defect.
	logDestinations = []struct{ path, name string }{
		{loggerPath, "NewWriterSink"},
		{slogPath, "NewTextHandler"},
		{slogPath, "NewJSONHandler"},
		{logPath, "New"},
		{logPath, "SetOutput"},
	}

	// writerFields are the struct field names that denote a log destination.
	writerFields = map[string]bool{
		"Writer": true, "Writers": true, "Out": true, "Output": true,
	}

	// The field name alone is NOT enough to conclude anything: a plain
	// `Report{Output: os.Stdout}` is a CLI writing its result, which ADR 0030
	// explicitly permits. Requiring the literal's TYPE to come from a logging
	// package is what keeps this rule on the destinations it is about. The cost is
	// that a consumer's own wrapper struct is missed — the documented price of
	// working without type resolution.
	loggingConfigPkgs = []string{loggerPath, slogPath, logPath}

	// legacyLogCalls are the stdlib log entry points that write through a pipeline
	// the SDK does not own. Fatal and Panic also bypass every deferred cleanup.
	legacyLogCalls = []string{
		"Print", "Printf", "Println",
		"Fatal", "Fatalf", "Fatalln",
		"Panic", "Panicf", "Panicln",
		"New", "SetOutput", "Default",
	}
)

// ruleEntity is one consumer-facing SDK convention and the check enforcing it.
//
// Unexported because a main package cannot be imported: nothing outside this
// binary can name the type, so exporting it would promise a surface that does
// not exist.
type ruleEntity struct {
	// ID is the stable identifier used in reports and suppressions.
	ID string
	// Title is the one-line statement of the rule.
	Title string
	// Level is LevelInvariant or LevelConvention.
	Level string
	// Source names the ADR or SDK rule the decision was recorded in.
	Source string
	// Check returns the findings for one parsed file.
	Check func(fc *fileCtx, file *ast.File) []findingEntity
}

// slogPipelineCalls are the log/slog entry points that BUILD a pipeline, as
// opposed to the vocabulary a bridge consumer legitimately needs.
//

// checkSlogPipeline implements SDK001.
func checkSlogPipeline(fc *fileCtx, file *ast.File) []findingEntity {
	if blind := fc.blindSpot(slogPath, "SDK001"); blind != nil {
		return blind
	}
	if !fc.imports(slogPath) {
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		for _, call := range slogPipelineCalls {
			if !fc.isCall(n, slogPath, call.name) {
				continue
			}
			// slog.New(slogbridge.NewHandler(lg)) composes the sanctioned
			// bridge by hand. It forwards into the one SDK pipeline, so
			// flagging it would fail an invariant on the very construction
			// this rule exists to steer people towards.
			if fc.wrapsBridge(n) {
				continue
			}
			out = append(out, fc.at(n, "SDK001", call.why+
				"; hand the SDK Logger over instead: slogbridge.New(lg) (ADR 0032)"))
		}
		return true
	})
	return out
}

// wrapsBridge reports whether a call's arguments come from the slog bridge.
//
// Both spellings count. The inline form is rare in practice because
// NewHandler returns (handler, error), so real code binds it first:
//
//	h, err := slogbridge.NewHandler(lg)
//	sl := slog.New(h)
//
// Missing that second form would have left the rule firing on the exact
// composition it tells people to write.
func (fc *fileCtx) wrapsBridge(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	for _, arg := range call.Args {
		// Inline: slog.New(slogbridge.NewHandler(lg)).
		if fc.isCall(arg, bridgePath, "NewHandler") || fc.isCall(arg, bridgePath, "New") {
			return true
		}
		// Bound: the identifier was assigned from a bridge constructor
		// earlier in this file.
		if ident, isIdent := arg.(*ast.Ident); isIdent && fc.bridgeBound[ident.Name] {
			return true
		}
	}
	return false
}

// checkUntypedErrors implements SDK002.
func checkUntypedErrors(fc *fileCtx, file *ast.File) []findingEntity {
	var out []findingEntity
	for path := range untypedErrorCalls {
		out = append(out, fc.blindSpot(path, "SDK002")...)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		for path, name := range untypedErrorCalls {
			if !fc.imports(path) || !fc.isCall(n, path, name) {
				continue
			}
			out = append(out, fc.at(n, "SDK002", path+"."+name+
				" yields an error with no code a caller can match on"+
				"; use errs.New / errs.Wrap from pkg/v1/errs (ADR 0019)"))
		}
		return true
	})
	return out
}

// loggingConfigPkgs are the packages whose struct literals configure logging.
//

// isLoggingConfig reports whether lit constructs a logging package's struct.
func (fc *fileCtx) isLoggingConfig(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	for _, path := range loggingConfigPkgs {
		if local, imported := fc.byPath[path]; imported && ident.Name == local {
			return true
		}
	}
	return false
}

// stdoutInConfig reports os.Stdout bound to a destination field of a logging
// package's struct literal.
func (fc *fileCtx) stdoutInConfig(lit *ast.CompositeLit) []findingEntity {
	if !fc.isLoggingConfig(lit) {
		return nil
	}
	var out []findingEntity
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || !writerFields[key.Name] {
			continue
		}
		out = append(out, fc.stdoutIn(kv.Value, "a "+key.Name+" field")...)
	}
	return out
}

// checkStdoutDestination implements SDK003.
//
// It fires only where os.Stdout becomes a LOG destination — a Writer-shaped
// struct field or a logging constructor's argument. Plain output
// (fmt.Fprintln(os.Stdout, …), json.NewEncoder(os.Stdout)) is untouched,
// because a CLI printing its results to stdout is doing its job; ADR 0030 is
// about stdout carrying a protocol, not about stdout being forbidden.
func checkStdoutDestination(fc *fileCtx, file *ast.File) []findingEntity {
	if blind := fc.blindSpot(osPath, "SDK003"); blind != nil {
		return blind
	}
	if !fc.imports(osPath) {
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			out = append(out, fc.stdoutInConfig(lit)...)
			return true
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, d := range logDestinations {
			if !fc.isCall(call, d.path, d.name) {
				continue
			}
			for _, arg := range call.Args {
				out = append(out, fc.stdoutIn(arg, d.name)...)
			}
		}
		return true
	})
	return out
}

// stdoutIn reports every os.Stdout reference inside expr, so a fan-out slice
// literal is caught as surely as a bare reference.
func (fc *fileCtx) stdoutIn(expr ast.Expr, where string) []findingEntity {
	var out []findingEntity
	ast.Inspect(expr, func(n ast.Node) bool {
		//: only an expression can be the os.Stdout selector.
		node, ok := n.(ast.Expr)
		if !ok || !fc.isSelector(node, osPath, "Stdout") {
			//: keep walking; a non-match says nothing about the subtree.
			return true
		}
		out = append(out, fc.at(n, "SDK003", "os.Stdout is used as "+where+
			"; under a stdio protocol a single log byte corrupts the stream"+
			" — log to os.Stderr, and name stdout explicitly only if it truly is free (ADR 0030)"))
		return true
	})
	return out
}

// checkVersionAssignment implements SDK004.
func checkVersionAssignment(fc *fileCtx, file *ast.File) []findingEntity {
	if blind := fc.blindSpot(loggerPath, "SDK004"); blind != nil {
		return blind
	}
	if !fc.imports(loggerPath) {
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			if !fc.isSelector(lhs, loggerPath, "Version") {
				continue
			}
			out = append(out, fc.at(lhs, "SDK004", "logger.Version is assigned at runtime"+
				"; stamp it at link time with -ldflags \"-X "+loggerPath+".Version=…\""+
				" (or Bazel --stamp) so a binary cannot misreport what built it"))
		}
		return true
	})
	return out
}

// checkLegacyLog implements SDK005.
func checkLegacyLog(fc *fileCtx, file *ast.File) []findingEntity {
	if blind := fc.blindSpot(logPath, "SDK005"); blind != nil {
		return blind
	}
	if !fc.imports(logPath) {
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		for _, name := range legacyLogCalls {
			if !fc.isCall(n, logPath, name) {
				continue
			}
			out = append(out, fc.at(n, "SDK005", "log."+name+
				" writes through the stdlib global logger, outside the SDK pipeline"+
				": no level, no framework_version, no sink the operator configured (ADR 0032)"))
		}
		return true
	})
	return out
}
