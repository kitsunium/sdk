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

// checkSlogPipeline implements SDK001.
func checkSlogPipeline(fc *fileCtx, file *ast.File) []findingEntity {
	//: a dot import hides every selector, so say so instead of passing quietly.
	if blind := fc.blindSpot(slogPath, "SDK001"); blind != nil {
		//: one honest finding beats a clean run this rule did not earn.
		return blind
	}
	//: the cheap gate: a file that never imports slog cannot build a pipeline.
	if !fc.imports(slogPath) {
		//: nothing here for this rule.
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		//: the table is ordered, so findings come out in a fixed order.
		for _, call := range slogPipelineCalls {
			//: only the constructors and the global build a destination.
			if !fc.isCall(n, slogPath, call.name) {
				//: not this entry point.
				continue
			}
			//: slog.New(slogbridge.NewHandler(lg)) composes the sanctioned
			//: bridge by hand. It forwards into the one SDK pipeline, so
			//: flagging it would fail an invariant on the very construction
			//: this rule exists to steer people towards.
			if fc.wrapsBridge(n) {
				//: sanctioned composition; nothing to report.
				continue
			}
			out = append(out, fc.at(n, "SDK001", call.why+
				"; hand the SDK Logger over instead: slogbridge.New(lg) (ADR 0032)"))
		}
		//: walk the whole file; a pipeline can be built anywhere in it.
		return true
	})
	//: every second destination this file builds.
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
	//: only a call carries arguments to inspect.
	if !ok {
		//: nothing wrapped, because nothing was called.
		return false
	}
	//: the handler may sit in any argument position.
	for _, arg := range call.Args {
		//: inline: slog.New(slogbridge.NewHandler(lg)).
		if fc.isCall(arg, bridgePath, "NewHandler") || fc.isCall(arg, bridgePath, "New") {
			//: the argument IS the bridge.
			return true
		}
		//: bound: the identifier was assigned from a bridge constructor
		//: earlier in this file.
		if ident, isIdent := arg.(*ast.Ident); isIdent && fc.bridgeBound[ident.Name] {
			//: the name was bound from the bridge, so this composition is it.
			return true
		}
	}
	//: an ordinary handler; this call builds a second destination.
	return false
}

// checkUntypedErrors implements SDK002.
func checkUntypedErrors(fc *fileCtx, file *ast.File) []findingEntity {
	var out []findingEntity
	//: this rule watches two packages, so either may be the blind spot.
	for path := range untypedErrorCalls {
		out = append(out, fc.blindSpot(path, "SDK002")...)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		//: fmt.Errorf and errors.New are the two the SDK replaced.
		for path, name := range untypedErrorCalls {
			//: gate on the import first, so an unrelated file costs one lookup.
			if !fc.imports(path) || !fc.isCall(n, path, name) {
				//: not one of the constructors this rule replaces.
				continue
			}
			out = append(out, fc.at(n, "SDK002", path+"."+name+
				" yields an error with no code a caller can match on"+
				"; use errs.New / errs.Wrap from pkg/v1/errs (ADR 0019)"))
		}
		//: walk the whole file; errors are minted anywhere.
		return true
	})
	//: every untyped error this file mints.
	return out
}

// isLoggingConfig reports whether lit constructs a logging package's struct.
func (fc *fileCtx) isLoggingConfig(lit *ast.CompositeLit) bool {
	sel, ok := lit.Type.(*ast.SelectorExpr)
	//: an unqualified literal names a local type, which says nothing here.
	if !ok {
		//: the field-name heuristic alone must not fire.
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	//: the qualifier must be a plain package name.
	if !ok {
		//: a nested selector is not a package qualifier.
		return false
	}
	//: three packages configure logging; any of them makes the literal one.
	for _, path := range loggingConfigPkgs {
		//: the qualifier must resolve to one of them in THIS file.
		if local, imported := fc.byPath[path]; imported && ident.Name == local {
			//: the literal's TYPE comes from a logging package.
			return true
		}
	}
	//: some other package's struct; its Output field is its own business.
	return false
}

// stdoutInConfig reports os.Stdout bound to a destination field of a logging
// package's struct literal.
func (fc *fileCtx) stdoutInConfig(lit *ast.CompositeLit) []findingEntity {
	//: the type gate is what keeps this off every struct with an Output field.
	if !fc.isLoggingConfig(lit) {
		//: a Report writing to stdout is a CLI doing its job.
		return nil
	}
	var out []findingEntity
	//: only keyed fields name a destination; positional ones are unnamed.
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		//: only a keyed element names the field it fills.
		if !ok {
			//: a positional element carries no field name to match.
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		//: only the writer-shaped fields denote a log destination.
		if !ok || !writerFields[key.Name] {
			//: some other field of the same struct.
			continue
		}
		out = append(out, fc.stdoutIn(kv.Value, "a "+key.Name+" field")...)
	}
	//: every stdout destination this literal wires up.
	return out
}

// stdoutBlindSpot reports the first dot import that would make SDK003 blind.
//
// EVERY package the rule reads a selector from counts, not just os. With
// `. "log"` and an ordinary os import, New(os.Stdout, "", 0) carries no
// qualified callee, so the destination match never fires and the rule reports a
// clean file it never actually analysed — which is the one outcome a rule must
// not produce.
func stdoutBlindSpot(fc *fileCtx) []findingEntity {
	//: os hides the destination, the logging packages hide the callee.
	for _, path := range append([]string{osPath}, loggingConfigPkgs...) {
		//: the first one found is enough; they all mean the same thing.
		if blind := fc.blindSpot(path, "SDK003"); blind != nil {
			//: name the import a reader has to change.
			return blind
		}
	}
	//: every package this rule needs resolves normally.
	return nil
}

// checkStdoutDestination implements SDK003.
//
// It fires only where os.Stdout becomes a LOG destination — a Writer-shaped
// struct field or a logging constructor's argument. Plain output
// (fmt.Fprintln(os.Stdout, …), json.NewEncoder(os.Stdout)) is untouched,
// because a CLI printing its results to stdout is doing its job; ADR 0030 is
// about stdout carrying a protocol, not about stdout being forbidden.
func checkStdoutDestination(fc *fileCtx, file *ast.File) []findingEntity {
	//: a dot import hides every selector, so say so instead of passing quietly.
	if blind := stdoutBlindSpot(fc); blind != nil {
		//: one honest finding beats a clean run this rule did not earn.
		return blind
	}
	//: the cheap gate: no os import means no os.Stdout to find.
	if !fc.imports(osPath) {
		//: nothing here for this rule.
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		//: a struct literal is one of the two shapes a destination takes.
		if lit, ok := n.(*ast.CompositeLit); ok {
			out = append(out, fc.stdoutInConfig(lit)...)
			//: its fields were just inspected; keep walking for nested ones.
			return true
		}
		call, ok := n.(*ast.CallExpr)
		//: the other shape is a constructor argument.
		if !ok {
			//: neither shape; nothing to weigh here.
			return true
		}
		//: only these constructors turn an io.Writer into a log destination.
		for _, dest := range logDestinations {
			//: match the callee before looking at what it was handed.
			if !fc.isCall(call, dest.path, dest.name) {
				//: an ordinary call; its arguments are its own business.
				continue
			}
			//: the writer may sit in any argument position.
			for _, arg := range call.Args {
				out = append(out, fc.stdoutIn(arg, dest.name)...)
			}
		}
		//: walk the whole file; a destination is wired anywhere.
		return true
	})
	//: every place this file points logging at stdout.
	return out
}

// stdoutIn reports every os.Stdout reference inside expr, so a fan-out slice
// literal is caught as surely as a bare reference.
func (fc *fileCtx) stdoutIn(expr ast.Expr, where string) []findingEntity {
	var out []findingEntity
	ast.Inspect(expr, func(n ast.Node) bool {
		//: only an expression can be the os.Stdout selector.
		node, ok := n.(ast.Expr)
		//: os.Stdout is a selector expression and nothing else.
		if !ok || !fc.isSelector(node, osPath, "Stdout") {
			//: keep walking; a non-match says nothing about the subtree.
			return true
		}
		out = append(out, fc.at(n, "SDK003", "os.Stdout is used as "+where+
			"; under a stdio protocol a single log byte corrupts the stream"+
			" — log to os.Stderr, and name stdout explicitly only if it truly is free (ADR 0030)"))
		//: keep descending: a fan-out slice holds more than one writer.
		return true
	})
	//: every os.Stdout inside this expression.
	return out
}

// checkVersionAssignment implements SDK004.
func checkVersionAssignment(fc *fileCtx, file *ast.File) []findingEntity {
	//: a dot import hides every selector, so say so instead of passing quietly.
	if blind := fc.blindSpot(loggerPath, "SDK004"); blind != nil {
		//: one honest finding beats a clean run this rule did not earn.
		return blind
	}
	//: the cheap gate: only a consumer of the facade can assign its Version.
	if !fc.imports(loggerPath) {
		//: nothing here for this rule.
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		//: reading Version is fine; only writing it defeats the link-time stamp.
		if !ok {
			//: not an assignment.
			return true
		}
		//: a multi-value assignment may write it in any position.
		for _, lhs := range assign.Lhs {
			//: only the package variable itself is the violation.
			if !fc.isSelector(lhs, loggerPath, "Version") {
				//: some other target.
				continue
			}
			out = append(out, fc.at(lhs, "SDK004", "logger.Version is assigned at runtime"+
				"; stamp it at link time with -ldflags \"-X "+loggerPath+".Version=…\""+
				" (or Bazel --stamp) so a binary cannot misreport what built it"))
		}
		//: walk the whole file; the assignment can sit in any init path.
		return true
	})
	//: every runtime write to the version stamp.
	return out
}

// checkLegacyLog implements SDK005.
func checkLegacyLog(fc *fileCtx, file *ast.File) []findingEntity {
	//: a dot import hides every selector, so say so instead of passing quietly.
	if blind := fc.blindSpot(logPath, "SDK005"); blind != nil {
		//: one honest finding beats a clean run this rule did not earn.
		return blind
	}
	//: the cheap gate: a file that never imports log cannot call through it.
	if !fc.imports(logPath) {
		//: nothing here for this rule.
		return nil
	}
	var out []findingEntity
	ast.Inspect(file, func(n ast.Node) bool {
		//: every entry point that writes through the stdlib global.
		for _, name := range legacyLogCalls {
			//: match the callee against the table of global entry points.
			if !fc.isCall(n, logPath, name) {
				//: not one of them.
				continue
			}
			out = append(out, fc.at(n, "SDK005", "log."+name+
				" writes through the stdlib global logger, outside the SDK pipeline"+
				": no level, no framework_version, no sink the operator configured (ADR 0032)"))
		}
		//: walk the whole file; the legacy logger is called anywhere.
		return true
	})
	//: every write this file makes through the stdlib global.
	return out
}
