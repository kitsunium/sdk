package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// environmentFormat says the variables a product reads; %[1]s is its
// variables' prefix.
const environmentFormat string = `
Environment:
  %[1]s_ENV=dev
      serve the Studio at /_app/ (default: production, no Studio)
  %[1]s_ADDR, PORT
      where to listen (dev: 127.0.0.1:4000, production: :4000)
  %[1]s_DATA_DIR
      where stores and queues persist (dev: .app/data)
  %[1]s_SMTP_URL
      where mailers deliver: smtp://user:password@host:587?tls=starttls,
      or %[1]s_SMTP_URL_FILE naming a file that holds it
      (unset: mail is captured for the Studio, not delivered)
  %[1]s_<NAME>
      a setting or a secret the product declares;
      a secret also %[1]s_<NAME>_FILE naming a file that holds it
  %[1]s_<DB>_URL
      the URL of the database <db> (app.Database), or %[1]s_<DB>_URL_FILE
  %[1]s_SECRETS
      where the environment keeps its secrets: env, memory or file:<dir>
      (unset: an encrypted store beside the data, or memory without data)
  %[1]s_SECRETS_KEY
      the key of a file store: 32 bytes in base64 (openssl rand -base64 32),
      or %[1]s_SECRETS_KEY_FILE naming a file that holds it
      (unset: a key file beside the store, made on first use)
  %[1]s_TRUST_PROXY
      on: app.ClientIP reads the last X-Forwarded-For address
  %[1]s_RETENTION
      what the stores' retention does: on (default), dry-run or off
  %[1]s_INDEX_KEY
      pins the framework's index key, which references a person's records
      (unset: made on first use and kept with the secrets; kept in memory
      while the data persists, it is warned of at every start)
  %[1]s_DATA_KEY
      pins the framework's data-key, which wraps the keys personal data is sealed under
      at rest: 32 bytes (unset: made on first use, kept with the secrets, rotated)
`

// usageFormat is Main's usage; %[1]s is the product's name.
const usageFormat string = `%[1]s

Usage:
  %[1]s [serve]                       run the product
  %[1]s graph [-format json|mermaid]  print the product graph
  %[1]s healthcheck                   exit 0 when the running product is ready
  %[1]s config                        every setting: its value and where it comes from
  %[1]s secrets [list]                every secret: where it is found, its version
  %[1]s secrets set NAME < file       a new version of NAME, read from standard input
  %[1]s secrets rotate NAME|-all      a new version of a secret the product generates, now
  %[1]s migrate [status]              every migration of the databases: applied or pending
  %[1]s migrate up                    apply every pending migration
  %[1]s migrate down SET VERSION      reverse SET's migrations above VERSION
  %[1]s privacy register|export|erase|holds|journal|retention|seal
                                      the personal data the product keeps (%[1]s privacy for more)
  %[1]s revisions restore -store ID -key KEY -rev N [-field PATH]…
                                      a record's version written back, as a new version
  %[1]s watch replay WATCH            one notice per record of each store that feeds WATCH
  %[1]s data copy --to DATABASE       the data directory copied into DATABASE, verified by fingerprints
`

// Main is the whole main function of a product:
//
//	func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
//
// It understands ten commands, so every binary of the framework can describe itself, be
// probed, say its configuration, manage its secrets, migrate its databases,
// answer for the personal data it keeps, replay what its watches hear and
// copy its data directory into a database without a shell or another tool in
// its image:
//
//	serve        run the product (the default)
//	graph        print the product graph: -format json|mermaid
//	healthcheck  exit 0 when the product running on <PRODUCT>_ADDR is ready
//	config       every setting: its value, never a secret's, and its origin
//	secrets      list the secrets, set one from standard input, rotate one
//	migrate      the databases' migrations: status, up, down SET VERSION
//	privacy      the register of processing, a person's export and erasure,
//	             the legal holds, the privacy journal, the retention
//	revisions    a record's version restored
//	watch        replay WATCH: one notice per record of each store that feeds it
//	data         the data directory copied into a database: data copy --to NAME,
//	             verified by fingerprints
//
// An app whose port declares a [Breaker] understands breaker too: list the
// breakers, isolate PORT — open its breaker until it is reset — and reset
// PORT, kept in the data directory for every start and a running product.
// An app that declares an [OTLP] export understands otlp: dead-letters
// lists the batches its collector refused for good, never their payload,
// and replay puts them back into the live queue, or with --drop deletes
// them.
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
	a.usageBreaker(os.Stderr)
	a.usageCommands(os.Stderr)
	return 2
}

// mainCommands are Main's own commands, by the word that runs each.
func (a *App) mainCommands() map[string]func(ctx context.Context, args []string) int {
	help := func(context.Context, []string) int {
		usage(os.Stdout, a.name)
		a.usageBreaker(os.Stdout)
		a.usageCommands(os.Stdout)
		return 0
	}
	cmds := map[string]func(ctx context.Context, args []string) int{
		"serve":       func(ctx context.Context, _ []string) int { return a.serveCommand(ctx) },
		"graph":       func(ctx context.Context, args []string) int { return a.graphCommand(ctx, args, os.Stdout, os.Stderr) },
		"healthcheck": func(ctx context.Context, _ []string) int { return a.healthcheck(ctx, os.Stderr) },
		"config":      func(ctx context.Context, args []string) int { return a.configCommand(ctx, args, os.Stdout, os.Stderr) },
		"secrets": func(ctx context.Context, args []string) int {
			return a.secretsCommand(ctx, args, os.Stdin, os.Stdout, os.Stderr)
		},
		// The commands on the stores are kit's own store code, which the
		// store subsystem plugs in (store_enable.go).
		"migrate": a.onStores("migrate"), "privacy": a.onStores("privacy"), "revisions": a.onStores("revisions"),
		"watch": a.onStores("watch"), "data": a.onStores("data"),
		"help": help, "-h": help, "-help": help, "--help": help,
	}
	if run := a.breakerCLI(); run != nil {
		cmds["breaker"] = run
	}
	if run := a.otlpCLI(); run != nil {
		cmds["otlp"] = run
	}
	return cmds
}

// breakerCLI is Main's `breaker` command, nil when no port of the app
// declares a breaker: reached through one (portBreaker.command), so a
// product that declares none links none of it.
func (a *App) breakerCLI() func(ctx context.Context, args []string) int {
	if bs := a.breakers(); len(bs) > 0 {
		return bs[0].b.command(a)
	}
	return nil
}

// usageBreaker says the `breaker` command, when the app has one, and the
// `otlp` command, when the app has one.
func (a *App) usageBreaker(w io.Writer) {
	if len(a.breakers()) > 0 {
		fmt.Fprintf(w, "  %[1]s breaker [list]|isolate PORT|reset PORT\n%[2]s a port's breaker, opened by an operator until reset\n", a.name, strings.Repeat(" ", 37))
	}
	if a.otlpCLI() != nil {
		fmt.Fprintf(w, "  %[1]s otlp dead-letters|replay [traces|metrics] [--drop] [ID...]\n%[2]s the OTLP export's dead letters, listed, replayed or dropped\n", a.name, strings.Repeat(" ", 37))
		fmt.Fprintf(w, "  %[1]s otlp migrate [traces|metrics]\n%[2]s the OTLP export's batches left under the data directory, moved onto otlp-database\n", a.name, strings.Repeat(" ", 37))
	}
}

// onStores is Main's command name on the stores, on the process's standard
// streams.
func (a *App) onStores(name string) func(ctx context.Context, args []string) int {
	return func(ctx context.Context, args []string) int {
		return a.storeCommand(ctx, name, args, os.Stdout, os.Stderr)
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
	if err := renamedRefusal(renamedVariables(a.name, os.Getenv)); err != nil {
		fmt.Fprintln(stderr, err)
		return exitConfig
	}
	cfg := resolveConfig(&a.opts, a.name, os.Getenv)
	if _, err := health.Ask(ctx, health.AskConfig{Addr: cfg.addr, Path: healthPrefix + "ready"}); err != nil {
		fmt.Fprintf(stderr, "%s is not ready: %s\n", a.name, errs.PublicOf(err))
		return 1
	}
	return 0
}
