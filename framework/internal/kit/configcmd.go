package kit

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/kitsunium/sdk/framework/model"
)

// configCommand prints what the product starts with, in the environment it
// runs in: every setting — kit's own, then those the services declare — its
// value, never a secret's, and where it comes from. It exits 1, with every
// problem said, when the start would refuse the configuration.
func (a *App) configCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "usage: %s config\n", a.name)
		return 2
	}
	// What the start says in dev only, config says everywhere (ADR 0006).
	resolveThenExplain := func() error {
		if err := a.resolve(); err != nil {
			return err
		}
		a.explainPrivacy()
		return nil
	}
	if err := firstError(resolveThenExplain, func() error { return a.openSecrets(ctx) }); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.closeSecrets()
	resolved := a.resolveSettings()
	a.mu.Lock()
	a.settingProblems = resolved.problems
	a.mu.Unlock()
	all := slices.Concat(settings(&a.opts, os.Getenv, &a.cfg), a.secretSettings(ctx), resolved.shown)
	if err := printSettings(stdout, all); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Every problem the start would say: the declarations, the secrets, the
	// mailers, the settings.
	status := 0
	for _, d := range a.declarationProblems() {
		fmt.Fprintf(stderr, "%s: %s\n", d.Severity, d.Message)
		if d.Severity == "error" {
			status = 1
		}
	}
	return status
}

// printSettings writes every setting as a table: its variable, its value —
// never a secret's —, where it comes from, and who declares it.
func printSettings(w io.Writer, all []model.Setting) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "VARIABLE\tVALUE\tFROM\tDECLARED BY")
	for i := range all {
		s := &all[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, shownSetting(s), settingOrigin(s), settingOwner(s))
	}
	return tw.Flush()
}

// shownSetting is what the table shows of a setting's value.
func shownSetting(s *model.Setting) string {
	switch {
	case s.Secret && s.From == model.SettingDefault:
		return "(unset)"
	case s.Secret:
		return "(secret)"
	case s.Value == "":
		return `""`
	default:
		return s.Value
	}
}

// settingOrigin is where a setting comes from, with the file or the option
// that gives it.
func settingOrigin(s *model.Setting) string {
	switch {
	case s.From == model.SettingFile:
		return s.From + " " + s.Detail
	case s.From == model.SettingOption && s.Option != "":
		return s.From + " " + s.Option
	default:
		return s.From
	}
}

// settingOwner is who declares a setting: a service, a database, or kit.
func settingOwner(s *model.Setting) string {
	switch {
	case s.Service != "":
		return s.Service + " · " + s.Key
	case s.Database != "":
		return "kit · database " + s.Database
	default:
		return "kit"
	}
}
