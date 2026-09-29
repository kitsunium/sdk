// Package kit — CLI commands: a short command of the product that Main runs
// once, in the CLI profile, and whose status is the process's.
package kit

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// CodeCLIFailed marks the span of a CLI command that exited non-zero.
const CodeCLIFailed errs.Code = 0x00_04_02_3B // 0.4.2.59 — a CLI command exited with a non-zero status

// exitSoftware is sysexits' EX_SOFTWARE: the status of a command that
// panicked.
const exitSoftware int = 70

// cliStopTimeout bounds the stop after a command.
const cliStopTimeout time.Duration = 10 * time.Second

// reservedCommands are Main's own: a product's CLI command may not take one.
var reservedCommands = map[string]bool{
	"serve": true, "graph": true, "healthcheck": true, "config": true, "secrets": true,
	"migrate": true, "privacy": true, "help": true,
}

// StdioValue is where a CLI command reads and writes: the process's own streams
// under Main, buffers in a test.
type StdioValue struct {
	// In is the command's standard input.
	In io.Reader
	// Out and Err are its standard output and error.
	Out, Err io.Writer
	// Interactive reports whether In is a terminal.
	Interactive bool
}

// CLIFunc is a CLI command's body: its arguments after its name, and the
// streams; it returns the process exit status (0 success, 64 usage, 70
// software — the sysexits a shell script branches on).
type CLIFunc func(ctx context.Context, args []string, std StdioValue) int

// CLI is a short command-line command of the product: it runs once, in the
// CLI profile, and exits with its status. Main runs it when the process's
// first argument is its name.
type CLI struct {
	nodeBase
	fn   CLIFunc
	doc  string
	path []string
	opts cliOptions
}

// CommandLineConfigurer tunes a CLI command.
type CommandLineConfigurer interface {
	// commandLineConfigure sets the option on o.
	commandLineConfigure(o *cliOptions)
}

// cliOptions are what the options set.
type cliOptions struct {
	// isDefault is DefaultCommand's: Main runs the command when the first
	// argument names no other command, or when there is none.
	isDefault bool
	// failSafe is FailSafe's: the command's status is always 0.
	failSafe bool
}

// cliOption is an option as a function.
type cliOption func(o *cliOptions)

// commandLineConfigure runs f on o.
func (f cliOption) commandLineConfigure(o *cliOptions) { f(o) }

// DefaultCommand makes the command Main's default: it runs with every
// argument when the first names no other command — neither one of the
// product's nor one of Main's (serve, help…) —, and with none when there
// is none. Main then never answers a usage error. One command of an app may
// be its default.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func DefaultCommand() CommandLineConfigurer {
	return cliOption(func(o *cliOptions) { o.isDefault = true })
}

// FailSafe makes the command's status always 0: a status line, a prompt
// hook, anything a shell runs on every keystroke must never fail the shell.
// A non-zero status, a start that fails and a panic are logged instead.
//
// IFACE-OPAQUE: CLIOption is sealed — its one method is unexported — so
// only this package makes one, and a caller only passes it to CLI.
func FailSafe() CommandLineConfigurer {
	return cliOption(func(o *cliOptions) { o.failSafe = true })
}

// CLI declares a command named name, which Main runs when the process is
// called "<binary> <name> ...". doc is its one-line help.
//
//go:noinline
func (s *Service) CLI(name, doc string, fn CLIFunc, opts ...CommandLineConfigurer) *CLI {
	c := &CLI{fn: fn, doc: doc, path: []string{name}}
	c.kind, c.name = model.KindCLI, name
	c.decl = callerPos()
	for _, o := range opts {
		if o != nil {
			o.commandLineConfigure(&c.opts)
		}
	}
	if p, _ := funcInfo(fn); p.file != "" {
		c.body = &p
	}
	s.add(c, true)
	if fn == nil {
		s.problem(c.decl, c.id, "cli.nil", "name", name)
	}
	return c
}

// describe draws the command: its words after the binary's name.
func (c *CLI) describe(_ *App, out *model.Node) []model.Edge {
	out.CLI = &model.CLIInfo{Path: c.path}
	return nil
}

// cliCommand is the CLI command named name among the app's, or nil.
func (a *App) cliCommand(name string) *CLI {
	for _, n := range a.mountedNodes() {
		if c, ok := n.(*CLI); ok && c.name == name {
			return c
		}
	}
	return nil
}

// defaultCLI is the app's default CLI command, or nil.
func (a *App) defaultCLI() *CLI {
	for _, n := range a.mountedNodes() {
		if c, ok := n.(*CLI); ok && c.opts.isDefault {
			return c
		}
	}
	return nil
}

// defaultProblems names a second default command: Main can run only one.
func (a *App) defaultProblems() []model.Diagnostic {
	var first *CLI
	var out []model.Diagnostic
	for _, n := range a.mountedNodes() {
		c, ok := n.(*CLI)
		switch {
		case !ok || !c.opts.isDefault:
		case first == nil:
			first = c
		default:
			out = append(out, diagnosticOf("error", c.id, a.source(&c.decl), say("cli.default-twice", "node", first.id, "other", c.id)))
		}
	}
	return out
}

// runCLI runs c in the CLI profile: the app starts what a command reads,
// the command runs inside its span, the app stops. A declaration error is 78,
// a start that fails its error's status, a panic 70 (exitSoftware); a
// fail-safe command's status is 0 whatever happened, the failure logged.
func (a *App) runCLI(ctx context.Context, c *CLI, args []string, stdio StdioValue) int {
	status := a.runCommand(ctx, c, args, stdio)
	if c.opts.failSafe && status != 0 {
		logger.Warn(ctx, a.log, "a fail-safe CLI command failed; its status is 0", logger.String("node", c.id),
			logger.Int("status", status))
		return 0
	}
	return status
}

// startFailed says why c could not start — on its standard error, or in the
// log for a fail-safe command, which must write nothing its shell reads —
// and returns the start's status.
func (a *App) startFailed(ctx context.Context, c *CLI, err error, stdio StdioValue) int {
	if c.opts.failSafe {
		logger.Warn(ctx, a.log, "a fail-safe CLI command could not start", logger.String("node", c.id),
			logger.String("error", errs.PublicOf(err)))
		return statusOf(err)
	}
	if _, werr := io.WriteString(stdio.Err, errs.PublicOf(err)+"\n"); werr != nil {
		logger.Warn(ctx, a.log, "the command's error could not be written", logger.String("error", werr.Error()))
	}
	return statusOf(err)
}

// runCommand starts the app, runs c in its span and stops the app.
func (a *App) runCommand(ctx context.Context, c *CLI, args []string, stdio StdioValue) (status int) {
	a.cliRun = true
	defer func() { a.cliRun = false }()
	if err := a.Start(ctx); err != nil {
		return a.startFailed(ctx, c, err, stdio)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cliStopTimeout)
		defer cancel()
		if err := a.stop(stopCtx, "the command ended"); err != nil {
			logger.Warn(stopCtx, a.log, "the app did not stop cleanly after a command", logger.String("node", c.id),
				logger.String("error", errs.PublicOf(err)))
		}
	}()
	ctx, sp := a.begin(a.baseCtx, &spanStart{node: c.id, op: model.OpCLI, name: c.name})
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a CLI command panicked", logger.String("node", c.id),
				logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			status = exitSoftware
			sp.end(failure(CodeHandlerPanic, "HANDLER_PANICKED", "a CLI command panicked", nil, errs.String("command", c.id)))
		}
	}()
	status = c.fn(withNode(ctx, c.id, a), args, stdio)
	var err error
	if status != 0 {
		err = failure(CodeCLIFailed, "CLI_FAILED", "the command exited with a non-zero status", nil, errs.Int("status", status))
	}
	sp.end(err)
	return status
}

// stdio is the process's own streams.
func stdio() StdioValue {
	interactive := false
	if fi, err := os.Stdin.Stat(); err == nil {
		interactive = fi.Mode()&os.ModeCharDevice != 0
	}
	return StdioValue{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Interactive: interactive}
}

// usageCommands lists the product's own CLI commands after Main's usage, one
// line each with its help; nothing when it declares none.
func (a *App) usageCommands(w io.Writer) {
	var cmds []*CLI
	for _, n := range a.mountedNodes() {
		if c, ok := n.(*CLI); ok {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return
	}
	if _, err := fmt.Fprintln(w, "\nCommands of the product:"); err != nil {
		return
	}
	for _, c := range cmds {
		mark := ""
		if c.opts.isDefault {
			mark = " (the default)"
		}
		if _, err := fmt.Fprintf(w, "  %s %-28s %s%s\n", a.name, c.name, c.doc, mark); err != nil {
			return
		}
	}
}
