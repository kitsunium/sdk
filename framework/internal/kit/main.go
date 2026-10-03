// Package kit — Main: the whole main function of a product.
package kit

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// environmentFormat says the variables a product reads; %[1]s is its
// variables' prefix.
const environmentFormat string = `
Environment:
  KIT_ENV=dev        serve the Studio at /_kit/ (default: production, no Studio)
  KIT_ADDR, PORT     where to listen (dev: 127.0.0.1:4000, production: :4000)
  KIT_DATA_DIR       where stores and queues persist (dev: .kit/data)
  KIT_SMTP_URL       where mailers deliver: smtp://user:password@host:587?tls=starttls,
                     or KIT_SMTP_URL_FILE naming a file that holds it
                     (unset: mail is captured for the Studio, not delivered)
  %[1]s_<NAME>        a setting or a secret the product declares;
                     a secret also %[1]s_<NAME>_FILE naming a file that holds it
  %[1]s_<DB>_URL      the URL of the database <db> (kit.Database), or %[1]s_<DB>_URL_FILE
  KIT_SECRETS        where the environment keeps its secrets: env, memory or file:<dir>
                     (unset: an encrypted store beside the data, or memory without data)
  KIT_SECRETS_KEY    the key of a file store: 32 bytes in base64 (openssl rand -base64 32),
                     or KIT_SECRETS_KEY_FILE naming a file that holds it
                     (unset: a key file beside the store, made on first use)
  KIT_TRUST_PROXY    on: kit.ClientIP reads the last X-Forwarded-For address
  KIT_RETENTION      what the stores' retention does: on (default), dry-run or off
  KIT_INDEX_KEY      pins kit's index key, which references a person's records
                     (unset: made on first use and kept with the secrets)
  KIT_DATA_KEY       pins kit's data-key, which wraps the keys personal data is sealed under
                     at rest: 32 bytes (unset: made on first use, kept with the secrets, rotated)
`

// usageFormat is Main's usage; %[1]s is the product's name.
const usageFormat string = `%[1]s — a product built with kit.

Usage:
  %[1]s [serve]                       run the product
  %[1]s graph [-format json|mermaid]  print the product graph
  %[1]s healthcheck                   exit 0 when the running product is ready
  %[1]s config                        every setting: its value and where it comes from
  %[1]s secrets [list]                every secret: where it is found, its version
  %[1]s secrets set NAME < file       a new version of NAME, read from standard input
  %[1]s secrets rotate NAME|-all      a new version of a secret kit generates, now
  %[1]s migrate [status]              every migration of the databases: applied or pending
  %[1]s migrate up                    apply every pending migration
  %[1]s migrate down SET VERSION      reverse SET's migrations above VERSION
  %[1]s privacy register|export|erase|holds|journal|retention|seal
                                      the personal data the product keeps (%[1]s privacy for more)
  %[1]s revisions restore -store ID -key KEY -rev N [-field PATH]…
                                      a record's version written back, as a new version
`

// Main is the whole main function of a product:
//
//	func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
//
// It understands eight commands, so every kit binary can describe itself, be
// probed, say its configuration, manage its secrets, migrate its databases
// and answer for the personal data it keeps without a shell or another tool
// in its image:
//
//	serve        run the product (the default)
//	graph        print the product graph: -format json|mermaid
//	healthcheck  exit 0 when the product running on KIT_ADDR is ready
//	config       every setting: its value, never a secret's, and its origin
//	secrets      list the secrets, set one from standard input, rotate one
//	migrate      the databases' migrations: status, up, down SET VERSION
//	privacy      the register of processing, a person's export and erasure,
//	             the legal holds, the privacy journal, the retention
//	revisions    a record's version restored
//
// A product's CLI command runs when the first argument names it. A command
// declared with DefaultCommand runs when the first argument names no command
// at all — with every argument —, and when there is none, in serve's place:
// Main then never answers a usage error.
//
// It returns the process exit status rather than exiting, so that deferred
// work runs and a test can call it.
func (a *App) Main(ctx context.Context, args []string) int {
	def := a.defaultCLI()
	if len(args) == 0 {
		if def != nil {
			return a.runCLI(ctx, def, nil, stdio())
		}
		return a.serveCommand(ctx)
	}
	cmd, rest := args[0], args[1:]
	if c := a.cliCommand(cmd); c != nil {
		return a.runCLI(ctx, c, rest, stdio())
	}
	if run, ok := a.mainCommands()[cmd]; ok {
		return run(ctx, rest)
	}
	if def != nil {
		return a.runCLI(ctx, def, args, stdio())
	}
	usage(os.Stderr, a.name)
	a.usageCommands(os.Stderr)
	return 2
}

// mainCommands are Main's own commands, by the word that runs each.
func (a *App) mainCommands() map[string]func(ctx context.Context, args []string) int {
	help := func(context.Context, []string) int {
		usage(os.Stdout, a.name)
		a.usageCommands(os.Stdout)
		return 0
	}
	return map[string]func(ctx context.Context, args []string) int{
		"serve":       func(ctx context.Context, _ []string) int { return a.serveCommand(ctx) },
		"graph":       func(ctx context.Context, args []string) int { return a.graphCommand(ctx, args, os.Stdout, os.Stderr) },
		"healthcheck": func(ctx context.Context, _ []string) int { return a.healthcheck(ctx, os.Stderr) },
		"config":      func(ctx context.Context, args []string) int { return a.configCommand(ctx, args, os.Stdout, os.Stderr) },
		"secrets": func(ctx context.Context, args []string) int {
			return a.secretsCommand(ctx, args, os.Stdin, os.Stdout, os.Stderr)
		},
		"migrate": func(ctx context.Context, args []string) int { return a.migrateCommand(ctx, args, os.Stdout, os.Stderr) },
		"privacy": func(ctx context.Context, args []string) int { return a.privacyCommand(ctx, args, os.Stdout, os.Stderr) },
		"revisions": func(ctx context.Context, args []string) int {
			return a.revisionsCommand(ctx, args, os.Stdout, os.Stderr)
		},
		"help": help, "-h": help, "-help": help, "--help": help,
	}
}

// serveCommand runs the product until it stops: 78 when it is declared
// wrong, the error's own exit status otherwise.
func (a *App) serveCommand(ctx context.Context) int {
	err := a.Run(ctx)
	if err == nil {
		return 0
	}
	fmt.Fprintln(os.Stderr, err)
	return statusOf(err)
}

// statusOf is the exit status of a run that failed: 78 when the product is
// declared wrong, the error's own status otherwise.
func statusOf(err error) int {
	if isDiagnostics(err) {
		return exitConfig
	}
	return errs.ExitCodeOf(err)
}

// usage prints Main's commands and the environment a product reads.
func usage(w io.Writer, name string) {
	fmt.Fprintf(w, usageFormat, name)
	usageEnvironment(w, name)
}

// usageEnvironment says the variables a product reads.
func usageEnvironment(w io.Writer, name string) {
	fmt.Fprintf(w, environmentFormat, appPrefix(name))
}

// graphCommand prints the graph of the product without running it: declared
// nodes and edges, enriched by the static analysis when the source is here.
func (a *App) graphCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "json", "json or mermaid")
	static := fs.Bool("static", true, "enrich the graph with the static analysis of the source")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *format != "json" && *format != "mermaid" {
		fmt.Fprintf(stderr, "unknown format %q: use json or mermaid\n", *format)
		return 2
	}
	if err := a.resolve(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *static && a.root != "" && a.opts.analyzer != nil {
		a.runAnalysis(ctx)
	}
	if err := writeGraph(stdout, a.Graph(), *format); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// writeGraph writes g to w as JSON, or as a Mermaid diagram.
func writeGraph(w io.Writer, g *model.Graph, format string) error {
	if format == "mermaid" {
		_, err := io.WriteString(w, model.Mermaid(g))
		return err
	}
	raw, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(raw, '\n'))
	return err
}

// healthcheck asks the running product whether it is ready. It is what a
// container's HEALTHCHECK runs, in an image with no shell and no curl. The
// SDK's health.Ask dials where the product listens — an unspecified host is
// this machine's loopback — within three seconds, and a product that is not
// ready says why on stderr: unreachable, too slow, or the status it answered.
func (a *App) healthcheck(ctx context.Context, stderr io.Writer) int {
	cfg := resolveConfig(&a.opts, os.Getenv)
	if _, err := health.Ask(ctx, health.AskConfig{Addr: cfg.addr, Path: "/_kit/health/ready"}); err != nil {
		fmt.Fprintf(stderr, "%s is not ready: %s\n", a.name, errs.PublicOf(err))
		return 1
	}
	return 0
}
