// Package kit — the secrets command.
package kit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/secret"
)

// The secrets command. The product's binary is the tool that manages its
// secrets, in dev as in a container: it knows what the product declares, and
// it reads the environment the product runs with — the same variables, the
// same store. `kit secrets` runs it in dev.

// maxSecretInput bounds a value read from standard input: a secret is a
// password, a token, a key or a certificate — kilobytes at most.
const maxSecretInput int64 = 64 << 10

// managedSecret is one secret the command manages: the product's, by the name
// it declared, or kit's own, by its name in the store (kit-smtp-url).
type managedSecret struct {
	// name is what the command shows and takes; stored, its name in the
	// environment's store — the same, for the product's.
	name, stored string
	// env reads its variable under envName — kit's own under its bare name,
	// smtp-url for KIT_SMTP_URL — and variable names it.
	env      secret.Store
	envName  string
	variable string
	// decl is the product's declaration; nil for kit's own.
	decl *Secret
}

// generated reports whether kit makes the secret.
func (m *managedSecret) generated() bool { return m.decl != nil && m.decl.opts.generated }

// secretsCommand runs `secrets list|set NAME|rotate NAME|-all`.
func (a *App) secretsCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	if err := firstError(a.resolve, func() error { return a.openSecrets(ctx) }); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer a.closeSecrets()
	all := a.managedSecrets()
	if len(all) == 0 {
		fmt.Fprintln(stderr, "the product declares no secret")
		return 1
	}
	return a.secretsSubcommand(ctx, all, &secretsCall{sub: sub, args: args, stdin: stdin, stdout: stdout, stderr: stderr})
}

// secretsCall is one call of the secrets command: its subcommand, its
// arguments and its streams.
type secretsCall struct {
	sub            string
	args           []string
	stdin          io.Reader
	stdout, stderr io.Writer
}

// secretsSubcommand runs the subcommand c names on the secrets all.
func (a *App) secretsSubcommand(ctx context.Context, all []managedSecret, c *secretsCall) int {
	switch {
	case c.sub == "list" && len(c.args) == 0:
		return a.listSecrets(ctx, all, c.stdout, c.stderr)
	case c.sub == "set" && len(c.args) == 1:
		return a.setSecret(ctx, all, c.args[0], c.stdin, c.stdout, c.stderr)
	case c.sub == "rotate" && len(c.args) == 1:
		return a.rotateSecrets(ctx, all, c.args[0], c.stdout, c.stderr)
	default:
		fmt.Fprintf(c.stderr, "usage: %[1]s secrets [list] | %[1]s secrets set NAME < file | %[1]s secrets rotate NAME|-all\n", a.name)
		return 2
	}
}

// managedSecrets lists the product's declared secrets and its databases'
// URLs, sorted, then kit's own when the product has a mailer.
func (a *App) managedSecrets() []managedSecret {
	st := a.secretsNow()
	prefix := appPrefix(a.name)
	var out []managedSecret
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if s, ok := n.(*Secret); ok {
				out = append(out, managedSecret{name: s.key(), stored: s.stored(), env: st.env, envName: s.stored(), variable: variableOf(prefix, s.key()), decl: s})
			}
		}
	}
	for _, d := range a.opts.databases {
		name := d.urlSecret()
		out = append(out, managedSecret{name: name, stored: name, env: st.env, envName: name, variable: secretVariable(prefix, name)})
	}
	slices.SortFunc(out, func(x, y managedSecret) int { return strings.Compare(x.name, y.name) })
	if a.hasMailer() {
		out = append(out, managedSecret{name: kitSecretPrefix + smtpSecret, stored: kitSecretPrefix + smtpSecret, env: st.kitEnv, envName: smtpSecret, variable: secretVariable("KIT", smtpSecret)})
	}
	return out
}

// listSecrets prints every secret: where it is found, its version, when it
// was made and when it rotates next — never a value.
func (a *App) listSecrets(ctx context.Context, all []managedSecret, stdout, stderr io.Writer) int {
	st := a.secretsNow()
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tMADE BY\tFOUND IN\tVARIABLE\tVERSION\tMADE\tNEXT ROTATION")
	status := 0
	for i := range all {
		m := &all[i]
		made := "operator"
		if m.generated() {
			made = "kit"
		}
		line, failed := st.secretLine(ctx, m)
		if failed {
			status = 1
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", m.name, made, line.found, m.variable, line.version, line.created, line.next)
	}
	if err := tw.Flush(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return status
}

// secretListing is what the secrets list says of where a secret is found:
// where, its version, when that version was made, when it rotates next.
type secretListing struct {
	found, version, created, next string
}

// secretLine is the list's line of m; failed when its store could not be
// read.
func (st *secretStores) secretLine(ctx context.Context, m *managedSecret) (line secretListing, failed bool) {
	line = secretListing{found: "nowhere", version: "-", created: "-", next: "-"}
	store, from, err := st.find(ctx, m.env, m.envName, m.stored)
	switch {
	case err == nil:
		line.found = from
		name := m.stored
		if from == model.SecretFromEnv {
			name = m.envName
		}
		m.versionLine(ctx, store, name, from, &line)
	case !errors.Is(err, secret.NotFound):
		line.found, failed = "unreadable: "+errs.PublicOf(err), true
	case m.generated() && st.kept != nil:
		line.found = "not made yet"
	}
	return line, failed
}

// versionLine fills what the list says of m's current version in store.
func (m *managedSecret) versionLine(ctx context.Context, store secret.Store, name, from string, line *secretListing) {
	versions, err := store.Versions(ctx, name)
	if err != nil || len(versions) == 0 {
		return
	}
	line.version = strconv.Itoa(versions[0].Version)
	if c := versions[0].Created; !c.IsZero() {
		line.created = c.UTC().Format(time.DateTime + " UTC")
	}
	if m.generated() && from != model.SecretFromEnv {
		line.next = versions[0].Created.Add(m.decl.opts.every).UTC().Format(time.DateTime + " UTC")
	}
}

// setSecret keeps a new version of one secret, read from standard input, in
// the environment's store. It refuses a terminal: what is typed there is
// echoed, and a pipe or a file keeps the value off the screen.
func (a *App) setSecret(ctx context.Context, all []managedSecret, name string, stdin io.Reader, stdout, stderr io.Writer) int {
	m, ok := findManaged(all, name)
	if !ok {
		fmt.Fprintf(stderr, "no secret named %q: %s secrets lists them\n", name, a.name)
		return 2
	}
	st := a.secretsNow()
	if st.kept == nil {
		fmt.Fprintf(stderr, "KIT_SECRETS=env keeps no store: set %s where the product runs\n", m.variable)
		return 1
	}
	if f, ok := stdin.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprintf(stderr, "the value is read from standard input, not typed: %s secrets set %s < file\n", a.name, name)
			return 2
		}
	}
	raw, err := io.ReadAll(io.LimitReader(stdin, maxSecretInput+1))
	defer clear(raw)
	if err != nil {
		fmt.Fprintln(stderr, "standard input cannot be read:", err)
		return 1
	}
	if int64(len(raw)) > maxSecretInput {
		fmt.Fprintf(stderr, "a secret holds at most %d KiB\n", maxSecretInput>>10)
		return 1
	}
	// One trailing line ending is the file's or echo's, not the secret's.
	value := bytes.TrimSuffix(bytes.TrimSuffix(raw, []byte("\n")), []byte("\r"))
	if len(value) == 0 {
		fmt.Fprintln(stderr, "standard input is empty: a secret has a value")
		return 1
	}
	v, err := st.kept.Put(ctx, m.stored, secret.New(value))
	if err != nil {
		fmt.Fprintln(stderr, "the store refused it:", errs.PublicOf(err))
		return 1
	}
	fmt.Fprintf(stdout, "%s: version %d\n", m.name, v.Version)
	if _, err := m.env.Get(ctx, m.envName); err == nil {
		fmt.Fprintf(stderr, "%s is set: the environment wins over the store until it is unset\n", m.variable)
	}
	return 0
}

// rotateSecrets makes a new version of one generated secret, or of every
// one, now.
func (a *App) rotateSecrets(ctx context.Context, all []managedSecret, name string, stdout, stderr io.Writer) int {
	targets, status := a.rotationTargets(all, name, stderr)
	if targets == nil {
		return status
	}
	st := a.secretsNow()
	if st.kept == nil {
		fmt.Fprintln(stderr, "KIT_SECRETS=env keeps no store kit can write")
		return 1
	}
	for i := range targets {
		if !a.rotateOne(ctx, st, &targets[i], stdout, stderr) {
			status = 1
		}
	}
	return status
}

// rotationTargets are the secrets name asks to rotate: every generated one
// for -all, else the one named, when kit makes it. When there are none, it
// says why and returns the status to exit with.
func (a *App) rotationTargets(all []managedSecret, name string, stderr io.Writer) ([]managedSecret, int) {
	if name == "-all" {
		var targets []managedSecret
		for i := range all {
			if all[i].generated() {
				targets = append(targets, all[i])
			}
		}
		if len(targets) == 0 {
			fmt.Fprintln(stderr, "the product generates no secret")
			return nil, 1
		}
		return targets, 0
	}
	m, ok := findManaged(all, name)
	switch {
	case !ok:
		fmt.Fprintf(stderr, "no secret named %q: %s secrets lists them\n", name, a.name)
		return nil, 2
	case !m.generated():
		fmt.Fprintf(stderr, "%s is given by the operator, not made by kit: %s secrets set %s\n", name, a.name, name)
		return nil, 2
	default:
		return []managedSecret{m}, 0
	}
}

// rotateOne makes a new version of m now, unless the environment pins it; it
// reports whether it did.
func (a *App) rotateOne(ctx context.Context, st *secretStores, m *managedSecret, stdout, stderr io.Writer) bool {
	if _, err := m.env.Get(ctx, m.envName); err == nil {
		fmt.Fprintf(stderr, "%s: pinned by %s, not rotated\n", m.name, m.variable)
		return false
	}
	o := m.decl.opts
	var v secret.Versioned
	rotator, err := secret.NewRotator(secret.RotatorConfig{
		Store: st.kept, Name: m.stored, Clock: a.clock,
		Policy: secret.Policy{Every: o.every, Keep: o.keep, Generate: secret.Random(o.bytes)},
	})
	if err == nil {
		// A version kept with a prune that failed is still a rotation.
		v, err = rotator.Rotate(ctx)
	}
	if v.Version > 0 {
		fmt.Fprintf(stdout, "%s: version %d\n", m.name, v.Version)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", m.name, errs.PublicOf(err))
		return false
	}
	return true
}

// findManaged is the managed secret named name among all.
func findManaged(all []managedSecret, name string) (managedSecret, bool) {
	i := slices.IndexFunc(all, func(m managedSecret) bool { return m.name == name })
	if i < 0 {
		return managedSecret{}, false
	}
	return all[i], true
}
