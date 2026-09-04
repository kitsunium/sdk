// Command sdkguard — the rule table.
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

// LevelInvariant marks a rule whose violation is a correctness defect: records
// silently dropped, a protocol stream corrupted, a binary misreporting itself.
const LevelInvariant string = "invariant"

// LevelConvention marks a rule the SDK offers rather than imposes. ADR 0019
// makes the error model available to downstreams; it does not oblige them. A
// team adopting the SDK incrementally runs -level=invariant first and turns
// conventions on when it is ready, instead of switching the whole tool off.
const LevelConvention string = "convention"

// Rule is one consumer-facing SDK convention and the check that enforces it.
type Rule struct {
	// ID is the stable identifier used in reports and suppressions.
	ID string
	// Title is the one-line statement of the rule.
	Title string
	// Level is LevelInvariant or LevelConvention.
	Level string
	// Source names the ADR or SDK rule the decision was recorded in.
	Source string
	// Check returns the findings for one parsed file.
	Check func(fc *fileCtx, file *ast.File) []Finding
}

// allRules is the full rule table, ordered by ID.
var allRules = []Rule{
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

// slogPipelineCalls are the log/slog entry points that BUILD a pipeline, as
// opposed to the vocabulary a bridge consumer legitimately needs.
//
// The distinction is the whole rule. Banning the log/slog import outright would
// be simpler and wrong: a consumer of slogbridge imports it to type a
// *slog.Logger field or to pass slog.String attrs to a foreign API. Only the
// constructors and the process-wide default create a second destination.
var slogPipelineCalls = map[string]string{
	"New":            "slog.New builds a logger the SDK pipeline does not own",
	"NewTextHandler": "slog.NewTextHandler builds a second encoder and destination",
	"NewJSONHandler": "slog.NewJSONHandler builds a second encoder and destination",
	"SetDefault":     "slog.SetDefault redirects the process-wide logger away from the SDK",
	"Default":        "slog.Default reaches for a logger the SDK never configured",
}

// checkSlogPipeline implements SDK001.
func checkSlogPipeline(fc *fileCtx, file *ast.File) []Finding {
	if !fc.imports(slogPath) {
		return nil
	}
	var out []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		for name, why := range slogPipelineCalls {
			if fc.isCall(n, slogPath, name) {
				out = append(out, fc.at(n, "SDK001", why+
					"; hand the SDK Logger over instead: slogbridge.New(lg) (ADR 0032)"))
			}
		}
		return true
	})
	return out
}

// untypedErrorCalls are the stdlib error constructors the SDK replaced.
var untypedErrorCalls = map[string]string{
	fmtPath:    "Errorf",
	errorsPath: "New",
}

// checkUntypedErrors implements SDK002.
func checkUntypedErrors(fc *fileCtx, file *ast.File) []Finding {
	var out []Finding
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

// logDestinations are the constructors whose io.Writer argument becomes a log
// destination. os.Stdout reaching any of them is the ADR 0030 defect.
var logDestinations = []struct{ path, name string }{
	{loggerPath, "NewWriterSink"},
	{slogPath, "NewTextHandler"},
	{slogPath, "NewJSONHandler"},
	{logPath, "New"},
	{logPath, "SetOutput"},
}

// writerFields are the struct field names that denote a log destination.
var writerFields = map[string]bool{
	"Writer": true, "Writers": true, "Out": true, "Output": true,
}

// checkStdoutDestination implements SDK003.
//
// It fires only where os.Stdout becomes a LOG destination — a Writer-shaped
// struct field or a logging constructor's argument. Plain output
// (fmt.Fprintln(os.Stdout, …), json.NewEncoder(os.Stdout)) is untouched,
// because a CLI printing its results to stdout is doing its job; ADR 0030 is
// about stdout carrying a protocol, not about stdout being forbidden.
func checkStdoutDestination(fc *fileCtx, file *ast.File) []Finding {
	if !fc.imports(osPath) {
		return nil
	}
	var out []Finding
	ast.Inspect(file, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if key, isIdent := kv.Key.(*ast.Ident); isIdent && writerFields[key.Name] {
				out = append(out, fc.stdoutIn(kv.Value, "a "+key.Name+" field")...)
			}
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
func (fc *fileCtx) stdoutIn(expr ast.Expr, where string) []Finding {
	var out []Finding
	ast.Inspect(expr, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if !ok || !fc.isSelector(e, osPath, "Stdout") {
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
func checkVersionAssignment(fc *fileCtx, file *ast.File) []Finding {
	if !fc.imports(loggerPath) {
		return nil
	}
	var out []Finding
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

// legacyLogCalls are the stdlib log entry points that write through a pipeline
// the SDK does not own. Fatal and Panic also bypass every deferred cleanup.
var legacyLogCalls = []string{
	"Print", "Printf", "Println",
	"Fatal", "Fatalf", "Fatalln",
	"Panic", "Panicf", "Panicln",
	"New", "SetOutput", "Default",
}

// checkLegacyLog implements SDK005.
func checkLegacyLog(fc *fileCtx, file *ast.File) []Finding {
	if !fc.imports(logPath) {
		return nil
	}
	var out []Finding
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
