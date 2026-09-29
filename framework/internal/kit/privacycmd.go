// Package kit — the privacy command.
package kit

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/health"
	"github.com/kitsunium/sdk/pkg/v1/vfs"
)

// liveProbeTimeout bounds the question to a running product.
const liveProbeTimeout time.Duration = 2 * time.Second

// privacyUsageFormat is the privacy command's usage; %[1]s is the
// product's name.
const privacyUsageFormat string = `usage:
  %[1]s privacy register [-json]         the record of processing (GDPR art. 30), as far as the code knows it
  %[1]s privacy export ID…               a person's data, as kit.Export gives it (art. 15, 20)
  %[1]s privacy erase -reason TEXT ID…   a person's erasure, as kit.Erase does it (art. 17)
  %[1]s privacy holds                    the records a legal hold keeps
  %[1]s privacy journal [-verify]        the privacy journal, and whether its chain holds
  %[1]s privacy retention [-dry-run]     the stores' retention, run once now
  %[1]s privacy seal STORE               sealing at rest (not yet: ADR 0006, step 3)
Every sub-command but register opens the data directory: stop the product first.
`

// The privacy command (ADR 0006 §12). The product's binary is the tool, as
// for its secrets: it knows what the product declares, and reads the
// environment the product runs with. `kit privacy …` runs it in dev.
//
//	privacy register [-json]           the record of processing, as far as the code knows it
//	privacy export ID…                 a person's data, as kit.Export gives it
//	privacy erase -reason TEXT ID…     a person's erasure, as kit.Erase does it
//	privacy holds                      the records a legal hold keeps
//	privacy journal [-verify]          the privacy journal, and whether its chain holds
//	privacy retention [-dry-run]       the stores' retention, run once now
//	privacy seal STORE                 (sealing: ADR 0006, step 3)
//
// A store belongs to one process: every sub-command but register opens the
// data directory itself, and refuses while the product answers on its
// address — the Studio's Privacy page does the same in dev, on the running
// product.

// privacyCommand runs `privacy …`.
func (a *App) privacyCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		privacyUsage(stderr, a.name)
		return 2
	}
	sub, args := args[0], args[1:]
	run, known := a.privacyCommands(stdout, stderr)[sub]
	if !known {
		privacyUsage(stderr, a.name)
		return 2
	}
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	a.explainPrivacy()
	return run(withActor(ctx, actorCLI), args)
}

// privacyCommands are the privacy command's subcommands, by their word.
func (a *App) privacyCommands(stdout, stderr io.Writer) map[string]func(ctx context.Context, args []string) int {
	return map[string]func(ctx context.Context, args []string) int{
		"register": func(ctx context.Context, args []string) int { return a.registerCommand(ctx, args, stdout, stderr) },
		"export":   func(ctx context.Context, args []string) int { return a.exportCommand(ctx, args, stdout, stderr) },
		"erase":    func(ctx context.Context, args []string) int { return a.eraseCommand(ctx, args, stdout, stderr) },
		"holds": func(ctx context.Context, _ []string) int {
			return a.onData(ctx, stderr, func(ctx context.Context) error { return a.printHolds(ctx, stdout) })
		},
		"journal":   func(ctx context.Context, args []string) int { return a.journalCommand(ctx, args, stdout, stderr) },
		"retention": func(ctx context.Context, args []string) int { return a.retentionCommand(ctx, args, stdout, stderr) },
		"seal": func(context.Context, []string) int {
			fmt.Fprintln(stderr, "privacy seal: nothing is sealed yet — sealing at rest lands with kit's per-subject data keys (ADR 0006, step 3)")
			return 1
		},
	}
}

// exportCommand prints a person's data, as kit.Export gives it.
func (a *App) exportCommand(ctx context.Context, ids []string, stdout, stderr io.Writer) int {
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		if len(ids) == 0 {
			return Invalid("usage: " + a.name + " privacy export ID…")
		}
		data, err := a.export(ctx, ids, false)
		if err != nil {
			return err
		}
		return writeIndented(stdout, data)
	})
}

// eraseCommand erases a person, as kit.Erase does, and says what it did.
func (a *App) eraseCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("privacy erase", flag.ContinueOnError)
	fs.SetOutput(stderr)
	reason := fs.String("reason", "", "why: journaled")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		if fs.NArg() == 0 || strings.TrimSpace(*reason) == "" {
			return Invalid("usage: " + a.name + " privacy erase -reason TEXT ID…")
		}
		done, err := a.erase(ctx, *reason, fs.Args())
		printErasure(stdout, done)
		return err
	})
}

// journalCommand prints the journal; -verify checks its chain.
func (a *App) journalCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("privacy journal", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verify := fs.Bool("verify", false, "check the chain, and exit 1 where it breaks")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		return a.printJournal(ctx, stdout, *verify)
	})
}

// retentionCommand runs every store's retention once, now; -dry-run
// journals what is due and changes nothing.
func (a *App) retentionCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("privacy retention", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "journal what is due, change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	mode := model.RetentionOn
	if *dry {
		mode = model.RetentionDryRun
	}
	return a.onData(ctx, stderr, func(ctx context.Context) error {
		return a.retainNow(ctx, mode, stdout)
	})
}

// privacyUsage prints the privacy command's subcommands.
func privacyUsage(w io.Writer, name string) {
	fmt.Fprintf(w, privacyUsageFormat, name)
}

// registerCommand prints the register, enriched by the static analysis when
// the source is here and an analyzer was given (Analyzer): the recipients
// the code shows.
func (a *App) registerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("privacy register", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print the register as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if a.root != "" && a.opts.analyzer != nil {
		a.runAnalysis(ctx)
	}
	reg := a.register(a.Graph())
	if *asJSON {
		if err := writeIndented(stdout, reg); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	writeRegister(stdout, reg)
	return 0
}

// onData runs fn with the product's stores open in this process — the
// environment's secrets, the data directory, every store — and closes them
// after. It refuses while the product answers on its address: a store
// belongs to one process.
func (a *App) onData(ctx context.Context, stderr io.Writer, fn func(context.Context) error) int {
	err := a.withData(ctx, fn)
	if err == nil {
		return 0
	}
	declaredWrong := isDiagnostics(err)
	switch {
	case declaredWrong:
		fmt.Fprintln(stderr, err)
		return exitConfig
	case errs.PublicOf(err) != "":
		fmt.Fprintln(stderr, errs.PublicOf(err))
	default:
		fmt.Fprintln(stderr, describeText(err))
	}
	return 1
}

// withData opens what fn needs, runs it, and closes it all.
func (a *App) withData(ctx context.Context, fn func(context.Context) error) (err error) {
	if err := a.dataReady(ctx); err != nil {
		return err
	}
	if err := a.openSecrets(ctx); err != nil {
		return err
	}
	defer a.closeSecrets()
	if err := a.mount(); err != nil {
		return err
	}
	defer a.unmount()
	fsys, err := vfs.NewOS(a.dataDir)
	if err != nil {
		return failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be opened", err, errs.String("dir", a.dataDir))
	}
	a.data = fsys
	started, err := a.startStores(ctx)
	defer func() {
		for _, st := range slices.Backward(started) {
			err = errors.Join(err, st.stop(context.WithoutCancel(ctx), a))
		}
	}()
	if err != nil {
		return err
	}
	return fn(ctx)
}

// dataReady refuses what the command must not open: data kept in memory, a
// data directory that holds nothing yet, a product that answers on its
// address — its stores belong to it —, declarations the start refuses.
func (a *App) dataReady(ctx context.Context) error {
	if a.dataDir == "" {
		return failure(CodePrivacyData, "PRIVACY_NO_DATA", "the product keeps its data in memory: there is nothing to open; set KIT_DATA_DIR", nil)
	}
	if info, err := os.Stat(a.dataDir); err != nil || !info.IsDir() {
		return failure(CodePrivacyData, "PRIVACY_NO_DATA", "the data directory holds nothing yet: there is nothing to open", err, errs.String("dir", a.dataDir))
	}
	if askErr := a.askLive(ctx); askErr == nil {
		return failure(CodePrivacyData, "PRIVACY_PRODUCT_RUNS",
			"the product answers on "+a.cfg.addr+": its stores belong to it — stop it first, or use the Studio's Privacy page in dev", nil)
	}
	var problems []model.Diagnostic
	for _, d := range a.declarationProblems() {
		if d.Severity == "error" {
			problems = append(problems, d)
		}
	}
	if len(problems) > 0 {
		return &DiagnosticsError{Diagnostics: problems}
	}
	return nil
}

// startStores opens the app's stores, service by service; it returns those
// it started, for the caller to stop, even when one fails.
func (a *App) startStores(ctx context.Context) ([]starter, error) {
	var started []starter
	for _, svc := range a.services {
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			st, ok := n.(starter)
			if !ok || n.base().kind != model.KindStore {
				continue
			}
			if err := st.start(ctx, a); err != nil {
				return started, err
			}
			started = append(started, st)
		}
	}
	return started, nil
}

// askLive asks the product's liveness probe, for at most liveProbeTimeout:
// nil when a product answers at the app's address.
func (a *App) askLive(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, liveProbeTimeout)
	defer cancel()
	_, err := health.Ask(probe, health.AskConfig{Addr: a.cfg.addr, Path: "/_kit/health/live"})
	return err
}
