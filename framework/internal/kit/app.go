// Package kit — the App: the services a product mounts, served by one process.
package kit

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/framework/telemetry"
	"github.com/kitsunium/sdk/pkg/v1/app/health"
	"github.com/kitsunium/sdk/pkg/v1/app/lifecycle"
	"github.com/kitsunium/sdk/pkg/v1/app/scheduler"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/vfs"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/observe/metrics"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
	"github.com/kitsunium/sdk/pkg/v1/proc/signal"
)

// kit's listener, on the SDK's server engine: its bounds on every phase of a
// request, the header phase's own — the one a slowloris stalls — and its
// headers' size.
const (
	httpReadHeader time.Duration = 10 * time.Second
	httpRead       time.Duration = 30 * time.Second
	httpWrite      time.Duration = 60 * time.Second
	httpIdle       time.Duration = 2 * time.Minute
	httpMaxHeader  int           = 1 << 20
	// httpDrain is how long a stop lets the requests in flight finish. The
	// lifecycle's own stop budget bounds it too.
	httpDrain time.Duration = 10 * time.Second
	// httpShards is how many listening sockets kit opens on its address:
	// one. The SDK's engine can shard an address with SO_REUSEPORT, which
	// would let a second process bind a busy port without an error and give
	// ":0" several ports — and does not exist on every platform.
	httpShards int = 1
)

// serverPackage is the package a server imports to serve HTTP.
const serverPackage = "github.com/kitsunium/sdk/framework/kit/server"

// App is the whole product: the services it mounts, served by one process.
// The same services can be mounted by several apps — one binary per service,
// or all of them in one — and each app draws exactly what it runs.
type App struct {
	name string
	// own are the services the product lists (NewApp); services every one
	// it runs, in start order: the modules' (module.go), then its own.
	own      []*Service
	services []*Service
	// modules are the modules it mounts, in start order (mount.go).
	modules []*mountedModule
	decl    pos
	opts    appOptions

	// Resolved when the app starts or describes itself.
	cfg     config
	clock   clock.Timed
	log     logger.Logger
	tracer  trace.Tracer
	hub     *hub
	data    vfs.FullFS
	dataDir string
	root    string
	module  string
	// build is what the binary was built from; devBuild, what kit dev said
	// about the build it launched (dev only).
	build    *model.Build
	devBuild *model.DevBuild
	baseCtx  context.Context

	mu         sync.Mutex
	phase      string
	startedAt  time.Time
	components []*model.Component
	loops      []*loopState
	problems   []model.Diagnostic
	frontends  []*Frontend
	static     *model.Graph
	analysis   *model.Analysis
	graphDirty *time.Timer
	revision   string
	// stopping is set, under mu, when Stop begins: no new graph notice may
	// start after it.
	stopping bool

	lc     lifecycle.Lifecycle
	health health.Health
	// streams ends every live event stream when the app starts draining: an
	// open Studio must not hold the HTTP shutdown for its whole budget.
	streams    context.Context
	endStreams context.CancelFunc
	// server is kit's listener on the SDK's engine — an interface, so a
	// product that serves no HTTP links none of the engine —; addr the
	// address it bound.
	server  httpEngine
	addr    string
	handler http.Handler
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// rt is the daemon's view of itself and the Studio's tools (daemon.go).
	rt runtimeState
	// secrets are the environment's secret stores, open while the app runs
	// (secretstore.go).
	secrets atomic.Pointer[secretStores]
	// settingValues are the settings the start resolved, by setting ID, and
	// settingProblems what it found wrong with them (setting.go).
	settingValues   atomic.Pointer[map[string]any]
	settingProblems []model.Diagnostic
	// wired is what the start decided each port calls (port.go), while the
	// services are mounted.
	wired atomic.Pointer[wiring]
	// privacy is what the app keeps for personal data: kit's own service,
	// the reference keys, the journal's chain (privacy.go).
	privacy privacyRun
	// turn is the data's writer turn: a transaction on the data directory
	// or in memory holds it alone (transact_turn.go).
	turn writerTurn
	// roots are where the modules' Go modules lie on this machine, for the
	// source endpoint (gomodule.go).
	roots moduleRoots
	// prof is what the process profiles keep while the app runs, and cliRun
	// says Main runs a CLI command (profiles.go).
	prof   profileState
	cliRun bool
	// binary is the binary a Role put the app in, and role its name there
	// (roles.go).
	binary *Binary
	role   string
	// tel is the telemetry exporter while the app runs with one
	// (telemetry.go).
	tel atomic.Pointer[telemetry.Exporter]
}

// DiagnosticsError is returned by Start when declarations are wrong. It lists
// every problem at once, each with its position.
type DiagnosticsError struct {
	Diagnostics []model.Diagnostic
}

// routes is a ServeMux that remembers which node owns which pattern.
type routes struct {
	mux    *http.ServeMux
	owners map[string]string
}

// componentGroups are the nodes of the app that are lifecycle components,
// by kind — the scheduler's jobs apart.
type componentGroups struct {
	secrets, stores, workflows, mailers, subs, commands, loops, listeners []node
	jobs                                                                  []*Job
}

// loopState is one loop of the daemon, as the Runtime view shows it.
type loopState struct {
	model.Loop
	schedule scheduler.Schedule
	// active counts the runs going on; the loop is running while it is not
	// zero.
	active int
}

// mountedModule is a module an app mounts, as its options resolve.
type mountedModule struct {
	module *Module
	prefix string
	// prefixAt is where the prefix was given.
	prefixAt pos
	// at is its last mount; nil when only a kit.Requires mounted it.
	at *pos
	// requiredBy are the mounted modules that require it.
	requiredBy []*Module
	// running is the prefix the module says while this app runs it.
	running *string
}

// NewApp declares the product named name, made of services. A module's
// services come with the module: [App.With] mounts it.
//
//go:noinline
func NewApp(name string, services ...*Service) *App {
	a := &App{name: name, own: services, decl: callerPos(), phase: model.PhaseStopped}
	a.modules, a.services = mounts(a.own, nil)
	return a
}

// With returns a new app: the same product, with the options of a and then
// opts — a module among them is mounted. a itself is left as it was, so the
// options a test gives the product's App — a kit.Set, a kit.Replace — end
// with that test.
//
//go:noinline
func (a *App) With(opts ...AppConfigurer) *App {
	b := &App{name: a.name, own: slices.Clone(a.own), decl: a.decl, phase: model.PhaseStopped, opts: a.opts.clone()}
	b.opts.withAt = callerPos()
	for _, o := range opts {
		if o != nil {
			o.appConfigure(&b.opts)
		}
	}
	b.opts.withAt = pos{}
	b.modules, b.services = mounts(b.own, b.opts.mounts)
	return b
}

// Name returns the product name.
func (a *App) Name() string { return a.name }

// running reports whether the app is started.
func (a *App) running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase == model.PhaseServing || a.phase == model.PhaseDraining
}

// URL returns the base URL the app listens on, once started.
func (a *App) URL() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.addr == "" {
		return ""
	}
	return "http://" + a.addr
}

// Handler returns the app's HTTP handler, once started: what httptest wraps.
func (a *App) Handler() http.Handler {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.handler
}

// resolve computes the configuration, the logger, the tracer and the source
// root. It is shared by Start and by describing the app without running it.
func (a *App) resolve() error {
	a.cfg = resolveConfig(&a.opts, os.Getenv)
	if a.cliRun {
		// A CLI command is not a product anybody looks at: no Studio, and no
		// question to git about the build, whatever the environment.
		a.cfg.studio = studioConfig{}
	}
	a.build, a.devBuild = describeBuild(a.cfg.env == EnvDev && !a.cliRun, os.Getenv)
	a.clock = a.cfg.clock
	a.dataDir = a.cfg.dataDir
	if a.dataDir != "" {
		abs, err := filepath.Abs(a.dataDir)
		if err != nil {
			return failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be resolved", err, errs.String("dir", a.dataDir))
		}
		a.dataDir = abs
	}
	a.hub = newHub(a.cfg.studio.on)
	a.hub.onStructure = a.graphChanged
	level := logger.LevelInfo
	if a.cfg.logLevel != "" {
		if l, err := logger.ParseLevel(a.cfg.logLevel); err == nil {
			level = l
		}
	}
	sink, err := logger.NewWriterSink(a.cfg.logs)
	if err != nil {
		return failure(CodeAppConfig, "LOGS_INVALID", "the log destination cannot be used", err)
	}
	enc := logger.NewJSONEncoder()
	if a.cfg.env == EnvDev {
		enc = logger.NewTextEncoder()
	}
	lg, err := logger.NewWithSink(logger.SinkConfig{Sink: sink, Encoder: enc, MinLevel: level})
	if err != nil {
		return failure(CodeAppConfig, "LOGS_INVALID", "the log destination cannot be used", err)
	}
	a.log = lg.With(logger.String("app", a.name))
	a.rt.plog = a.productLogger(sink, enc, level)
	a.tracer = trace.NewTracer(trace.TracerConfig{
		Resource: trace.Resource{Attrs: []trace.Attr{metrics.String(trace.ServiceNameKey, a.name)}},
		Scope:    trace.Scope{Name: "github.com/kitsunium/sdk/framework/internal/kit", Version: kitVersion()},
		Clock:    a.clock,
	})
	a.root, a.module = findModule(a.decl.file())
	// kit's own service first, when the app keeps personal data (privacy.go).
	a.mountPrivacy()
	return nil
}

// findModule walks up from the file that declared the app to the go.mod that
// owns it. It returns nothing under -trimpath, where files are not paths.
func findModule(file string) (root, module string) {
	if bi := readBuild(); bi != nil {
		module = bi.Main.Path
	}
	if !filepath.IsAbs(file) {
		return "", module
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		raw, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			return dir, cmp.Or(moduleLine(raw), module)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", module
		}
	}
}

// source relativizes a runtime position to the module root — or, for a
// module's code, to its own Go module's (gomodule.go).
func (a *App) source(p *pos) *model.Source {
	if p == nil || p.file() == "" {
		return nil
	}
	if s := a.goModuleSource(p); s != nil {
		return s
	}
	return &model.Source{File: a.relativeFile(p.file()), Line: p.line(), Func: p.fn()}
}

// relativeFile is file as the graph names it: under the product's root when
// the app knows it, else under its module path.
func (a *App) relativeFile(file string) string {
	slashed := filepath.ToSlash(file)
	switch {
	case a == nil:
		return slashed
	case a.root != "":
		if rel, err := filepath.Rel(a.root, file); err == nil && filepath.IsLocal(rel) {
			return filepath.ToSlash(rel)
		}
		return slashed
	case a.module != "":
		return strings.TrimPrefix(slashed, a.module+"/")
	default:
		return slashed
	}
}

// declarationProblems collects every declaration problem of every service,
// plus what can only be judged once declarations are complete.
func (a *App) declarationProblems() []model.Diagnostic {
	var out []model.Diagnostic
	seen := map[string]bool{}
	for _, svc := range a.services {
		out = append(out, a.serviceProblems(svc, seen)...)
	}
	for _, w := range a.cfg.warnings {
		out = append(out, diagnosticOf("warning", "", nil, w))
	}
	if g := a.componentNodes(); a.cfg.memoryData && len(g.stores)+len(g.subs)+len(g.commands)+len(g.mailers) > 0 {
		// Said only of an app that keeps something: a daemon with no store
		// loses nothing at a restart.
		out = append(out, diagnosticOf("warning", "", nil, say("config.memory")))
	}
	if a.cfg.studio.on && a.serves() && studioMount.Load() == nil {
		out = append(out, diagnosticOf("warning", "", nil, say("config.studio-missing", "package", studioPackage)))
	}
	return slices.Concat(out, a.moduleProblems(), a.appProblems(), a.serverProblems())
}

// serviceProblems are what the service declares wrong, and a second service
// of a name seen already.
func (a *App) serviceProblems(svc *Service, seen map[string]bool) []model.Diagnostic {
	if svc == nil {
		return []model.Diagnostic{diagnosticOf("error", "", nil, say("app.nil-service"))}
	}
	var out []model.Diagnostic
	if seen[svc.name] {
		out = append(out, diagnosticOf("error", svc.name, nil, say("app.service-twice", "service", svc.name)))
	}
	seen[svc.name] = true
	nodes, diags := svc.snapshot()
	for _, d := range diags {
		out = append(out, diagnosticOf(d.severity, d.node, a.source(&d.at), d.message))
	}
	for _, n := range nodes {
		c, ok := n.(interface{ check() []phrase })
		if !ok {
			continue
		}
		for _, p := range c.check() {
			out = append(out, diagnosticOf("error", n.base().id, a.source(&n.base().decl), p))
		}
	}
	return out
}

// diagnosticOf is a diagnostic the graph carries: kit's sentence in English,
// and in every language kit speaks for the Studio.
func diagnosticOf(severity, node string, at *model.Source, p phrase) model.Diagnostic {
	return model.Diagnostic{Severity: severity, Node: node, Source: at, Message: p.String(), Texts: p.texts}
}

// Error lists every problem the start found, one per line.
func (e *DiagnosticsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kit: the product has %d problem(s):", len(e.Diagnostics))
	for _, d := range e.Diagnostics {
		b.WriteString("\n  ")
		if d.Source != nil {
			fmt.Fprintf(&b, "%s:%d: ", d.Source.File, d.Source.Line)
		}
		b.WriteString(d.Message)
	}
	return b.String()
}

// Start brings the product up: it checks every declaration, opens the data
// directory, loads the stores, starts the consumers, the scheduler and the
// HTTP listener — in that order, through the SDK lifecycle, so a failure
// unwinds what already started. It returns once the product serves.
func (a *App) Start(ctx context.Context) error {
	if err := a.beginStart(); err != nil {
		return err
	}
	// Each step is timed and remembered: the Studio draws the start as a
	// sequence, as it ran.
	var mux *http.ServeMux
	if err := firstError(
		func() error {
			return a.step(model.BootConfig, func() (int, string, error) { return a.bootConfig(ctx) })
		},
		func() error { return a.step(model.BootDeclarations, a.bootDeclarations) },
		func() error {
			return a.step(model.BootMount, func() (int, string, error) {
				a.wire()
				return len(a.services), "", a.mount()
			})
		},
		func() error {
			return a.step(model.BootRoutes, func() (n int, value string, err error) {
				mux, n, err = a.bootRoutes()
				return n, "", err
			})
		},
		func() error { return a.step(model.BootData, a.bootData) },
	); err != nil {
		return a.failStart(err)
	}
	if err := a.step(model.BootHandler, func() (int, string, error) { return 0, "", a.bootHandler(ctx, mux) }); err != nil {
		a.cancel()
		return a.failStart(err)
	}
	a.lc = lifecycle.New(lifecycle.Config{Clock: a.clock, StopTimeout: 10 * time.Second, OnTransition: a.onTransition})
	if err := a.addComponents(); err != nil {
		return a.failStart(err)
	}
	a.mu.Lock()
	a.startedAt = a.clock.Now().UTC()
	n := len(a.components)
	a.mu.Unlock()
	if err := firstError(
		func() error {
			return a.step(model.BootComponents, func() (int, string, error) { return n, "", a.lc.Start(ctx) })
		},
		func() error { return a.step(model.BootServing, func() (int, string, error) { return 0, "", nil }) },
	); err != nil {
		a.cancel()
		return a.failStart(err)
	}
	a.setPhase(model.PhaseServing, "")
	if !a.cliRun {
		// A CLI command's streams are its own: kit says nothing on them.
		a.announce(ctx)
	}
	return nil
}

// beginStart moves the app to starting and forgets the previous run, or
// refuses a start of an app that runs.
func (a *App) beginStart() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.phase == model.PhaseStarting || a.phase == model.PhaseServing || a.phase == model.PhaseDraining {
		return failure(CodeAppRunning, "APP_RUNNING", "the app is already running", nil)
	}
	a.phase = model.PhaseStarting
	a.prof.asked, a.prof.askOnce = make(chan struct{}), &sync.Once{}
	a.rememberPhase(model.PhaseStarting, "")
	a.problems, a.components, a.loops, a.frontends, a.static, a.analysis = nil, nil, nil, nil, nil, nil
	a.stopping, a.graphDirty = false, nil
	a.rt.newRun()
	return nil
}

// failStart undoes a start that failed with err, and returns err.
func (a *App) failStart(err error) error {
	reason := a.failReason(err)
	a.closeSecrets()
	a.unmount()
	a.setPhase(model.PhaseFailed, reason)
	return err
}

// bootConfig resolves the configuration, opens the secrets and resolves the
// settings, and says how many there are.
func (a *App) bootConfig(ctx context.Context) (count int, value string, err error) {
	if err := firstError(a.resolve, func() error { return a.openSecrets(ctx) }); err != nil {
		return 0, "", err
	}
	resolved := a.resolveSettings()
	a.settingValues.Store(&resolved.values)
	set := slices.Concat(settings(&a.opts, os.Getenv, &a.cfg), a.secretSettings(ctx), resolved.shown)
	a.mu.Lock()
	a.rt.config = set
	a.settingProblems = resolved.problems
	a.mu.Unlock()
	return len(set), "", nil
}

// bootDeclarations refuses the start when a declaration has an error.
func (a *App) bootDeclarations() (count int, value string, err error) {
	all := a.declarationProblems()
	var errorsFound []model.Diagnostic
	for _, d := range all {
		if d.Severity == "error" {
			errorsFound = append(errorsFound, d)
		}
	}
	if len(errorsFound) > 0 {
		return len(errorsFound), "", &DiagnosticsError{Diagnostics: errorsFound}
	}
	return len(all), "", nil
}

// bootRoutes builds the router of the app's routes, or refuses the ones that
// cannot be served.
func (a *App) bootRoutes() (*http.ServeMux, int, error) {
	m, n, routeProblems := a.routes()
	if len(routeProblems) > 0 {
		return m, n, &DiagnosticsError{Diagnostics: routeProblems}
	}
	return m, n, nil
}

// bootData opens the data directory, when the app keeps its data in one.
func (a *App) bootData() (count int, value string, err error) {
	a.data = nil
	if a.dataDir == "" {
		return 0, "memory", nil
	}
	if a.cliRun && len(a.componentNodes().stores) == 0 {
		// A CLI command whose app keeps no store touches no directory.
		return 0, "none", nil
	}
	if err := os.MkdirAll(a.dataDir, 0o700); err != nil {
		return 0, a.dataDir, failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be created", err, errs.String("dir", a.dataDir))
	}
	fsys, err := vfs.NewOS(a.dataDir)
	if err != nil {
		return 0, a.dataDir, failure(CodeAppConfig, "DATA_DIR_INVALID", "the data directory cannot be opened", err, errs.String("dir", a.dataDir))
	}
	a.data = fsys
	return 0, a.dataDir, nil
}

// bootHandler makes the run's contexts and health probes, and the handler
// that serves mux — the Studio's routes in dev — behind kit's protections.
func (a *App) bootHandler(ctx context.Context, mux *http.ServeMux) error {
	a.baseCtx, a.cancel = context.WithCancel(context.WithoutCancel(ctx))
	a.streams, a.endStreams = context.WithCancel(a.baseCtx)
	a.health = health.New(health.Config{Clock: a.clock})
	serving := func(context.Context) error {
		if p := a.currentPhase(); p != model.PhaseServing {
			return Unavailable("the product is " + p)
		}
		return nil
	}
	if err := a.health.AddReadiness(health.ReadinessCheck{Name: "kit", Check: serving}); err != nil {
		return err
	}
	if err := a.health.AddLiveness(health.LivenessCheck{Name: "kit", Check: func() error { return nil }}); err != nil {
		return err
	}
	if a.cliRun {
		// Nothing answers HTTP in a CLI run: no probe routes, no handler.
		return nil
	}
	a.mountHealth(mux)
	if mount := studioMount.Load(); mount != nil && a.cfg.studio.on && a.serves() {
		(*mount)(a, mux)
	}
	a.handler = a.observeHTTP(a.protect(mux))
	return nil
}

// announce says the app serves, where the Studio is in dev, and every
// warning the start found.
func (a *App) announce(ctx context.Context) {
	if a.serves() {
		logger.Info(ctx, a.log, "serving", logger.String("url", a.URL()), logger.String("env", a.cfg.env))
	} else {
		logger.Info(ctx, a.log, "running", logger.String("profile", a.runProfile()), logger.String("env", a.cfg.env))
	}
	if a.cfg.studio.on {
		// The Studio is kit's own process (ADR 0010, D8): the product says
		// where its read routes are, and who shows them.
		logger.Info(ctx, a.log, "the product, as a diagram: kit dev prints the Studio's link", logger.String("api", a.URL()+"/_kit/api/"))
	}
	for _, d := range a.declarationProblems() {
		logger.Warn(ctx, a.log, d.Message, logger.String("node", d.Node))
	}
}

// step times one step of the start and remembers it, for the Studio's
// sequence of the start; the step's error is returned as it is.
func (a *App) step(name string, run func() (count int, value string, err error)) error {
	begun := time.Now()
	count, value, err := run()
	s := model.BootStep{
		Name:   name,
		Count:  count,
		Value:  value,
		Begun:  begun.UTC(),
		TookMs: round2(float64(time.Since(begun)) / float64(time.Millisecond)),
	}
	if err != nil {
		_, body := describe(err)
		s.Error = body.Message
	}
	a.mu.Lock()
	a.rt.boot = append(a.rt.boot, s)
	a.mu.Unlock()
	return err
}

// mount binds every service to this app. A service runs in one app at a
// time: its stores hold one app's data.
func (a *App) mount() error {
	for i, svc := range a.services {
		if !svc.app.CompareAndSwap(nil, a) {
			for _, prev := range a.services[:i] {
				prev.app.CompareAndSwap(a, nil)
			}
			return failure(CodeAppRunning, "SERVICE_RUNNING", "a service of this app is already running in another app", nil, errs.String("service", svc.name))
		}
	}
	a.mountModules()
	return nil
}

// unmount releases the app's services, modules and wiring, so another app can
// mount them.
func (a *App) unmount() {
	for _, svc := range a.services {
		if svc != nil {
			svc.app.CompareAndSwap(a, nil)
		}
	}
	a.unmountModules()
	a.wired.Store(nil)
}

// routes builds the HTTP routes of every endpoint and frontend, and says how
// many. A conflict between two routes — which net/http reports by panicking —
// becomes a diagnostic naming both.
func (a *App) routes() (*http.ServeMux, int, []model.Diagnostic) {
	r := &routes{mux: http.NewServeMux(), owners: map[string]string{}}
	var problems []model.Diagnostic
	for _, svc := range a.services {
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if f, ok := n.(*Frontend); ok && f.fsys != nil {
				a.frontends = append(a.frontends, f)
			}
			m, ok := n.(mounter)
			if !ok {
				continue
			}
			if problem := m.mount(a, r); !problem.empty() {
				problems = append(problems, diagnosticOf("error", n.base().id, a.source(&n.base().decl), problem))
			}
		}
	}
	return r.mux, len(r.owners), problems
}

// handle registers a route and returns "" — or, when net/http refuses it,
// the problem, naming the other route's owner.
func (r *routes) handle(pattern, owner string, h http.Handler) (problem phrase) {
	defer func() {
		if p := recover(); p != nil {
			other := ""
			for pat, o := range r.owners {
				if strings.Contains(fmt.Sprint(p), pat) {
					other = o
				}
			}
			if other == "" {
				problem = say("route.conflict", "route", pattern, "owner", owner, "detail", p)
			} else {
				problem = say("route.conflict-with", "route", pattern, "owner", owner, "other", other, "detail", p)
			}
		}
	}()
	r.mux.Handle(pattern, h)
	r.owners[pattern] = owner
	return phrase{}
}

// protect wraps the product's routes: cross-origin browser writes are
// refused (CSRF), and a panic escaping a handler becomes a 500 with nothing
// of the panic in the body.
func (a *App) protect(h http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusForbidden, wireError{Error: wireBody{Code: WireForbidden, Message: "cross-origin request refused"}})
	}))
	protected := cop.Handler(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logger.Error(r.Context(), a.log, "panic serving a request", logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
				writeJSON(w, http.StatusInternalServerError, wireError{Error: wireBody{Code: WireInternal, Message: "internal error"}})
			}
		}()
		protected.ServeHTTP(w, r)
	})
}

// addComponents registers the lifecycle components, in dependency order:
// secrets, databases, stores, workflows, mailers (before anything that may
// send), subscriptions — watches among them, which the stores started
// before them feed — and queued commands, the scheduler, the loops, the
// stores' retention, the HTTP listener, and in dev the static analysis. A
// command that is not queued is no component.
func (a *App) addComponents() error {
	g := a.componentNodes()
	// The singleton first: a second process refuses before it opens anything.
	// A CLI run never takes it: `<binary> daemon status` is useful exactly
	// while the daemon holds it. Secrets next: whatever starts after them may
	// use one. Then the databases, before the stores they keep.
	cli := a.runProfile() == model.ProfileCLI
	singleton := a.addSingleton
	if cli {
		singleton = func() error { return nil }
	}
	if err := firstError(singleton, func() error { return a.startGroups(g.secrets) }, a.addDatabases); err != nil {
		return err
	}
	if cli {
		// A CLI command reads and writes the stores; nothing that outlives it
		// starts — no consumer, no scheduler, no loop, no listener, no HTTP,
		// no health probe, no telemetry.
		return a.startGroups(g.stores)
	}
	return firstError(
		func() error { return a.startGroups(g.stores, g.workflows, g.mailers, g.subs, g.commands) },
		func() error { return a.addScheduler(g.jobs, g.workflows) },
		func() error { return a.startGroups(g.loops, g.listeners) },
		// The stores' retention loops, once whatever their writes wake is up
		// (retention.go).
		a.addRetention,
		a.addServing,
	)
}

// addSingleton adds the singleton lock, when the app declares one.
func (a *App) addSingleton() error {
	if a.opts.singleton == "" {
		return nil
	}
	return a.component("singleton", "", a.takeSingleton, a.releaseSingleton)
}

// addHealth adds the health probes, the last component: the SDK's Start
// latches the startup probe once everything before it is up, and its Stop —
// the first to run — turns readiness off before anything else stops.
func (a *App) addHealth() error {
	probe := health.Component(a.health, "health")
	return a.component(probe.Name, "", probe.Start, probe.Stop)
}

// firstError runs steps in order, and stops at the first that fails.
func firstError(steps ...func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// addServing registers what comes last: the HTTP listener, in dev the static
// analysis, and the health probe.
func (a *App) addServing() error {
	return firstError(
		a.addHTTP,
		func() error { return a.component("telemetry", "", a.startTelemetry, a.stopTelemetry) },
		a.addIdle,
		a.addAnalysis,
		a.addHealth,
	)
}

// addHTTP adds the HTTP server, when the run serves one.
func (a *App) addHTTP() error {
	start := httpStart.Load()
	if !a.serves() || start == nil {
		// A server without framework/kit/server was refused with the
		// declarations (serverProblems).
		return nil
	}
	return a.component("http", "", func(ctx context.Context) error { return (*start)(a, ctx) }, a.stopHTTP)
}

// httpEngine is what kit asks of the HTTP engine once it runs: the SDK's
// server, which framework/kit/server plugs in.
type httpEngine = plug.HTTPServer

// httpStart starts the HTTP server — startHTTP — once framework/kit/server
// is imported, and is nil before: a daemon or a CLI links none of the
// engine.
var httpStart atomic.Pointer[func(a *App, ctx context.Context) error]

// EnableServer makes an app in the server profile serve HTTP.
// framework/kit/server calls it as it is imported; a product never does.
func EnableServer() {
	start := (*App).startHTTP
	httpStart.Store(&start)
}

// serverProblems refuses a server that did not import framework/kit/server:
// it would start and answer nothing.
func (a *App) serverProblems() []model.Diagnostic {
	if a.runProfile() != model.ProfileServer || httpStart.Load() != nil {
		return nil
	}
	return []model.Diagnostic{diagnosticOf("error", "", nil, say("profile.server-missing", "package", serverPackage))}
}

// addIdle adds the idle watch, when the daemon declares an idle stop.
func (a *App) addIdle() error {
	if a.opts.idle <= 0 {
		return nil
	}
	return a.component("idle", "", a.startIdle, func(context.Context) error { return nil })
}

// addAnalysis adds the static analysis, when the Studio runs one and the app
// was given an analyzer and knows its source.
func (a *App) addAnalysis() error {
	if !a.serves() || !a.cfg.studio.analyze || a.root == "" || a.opts.analyzer == nil {
		return nil
	}
	return a.component("analysis", "", a.startAnalysis, func(context.Context) error { return nil })
}

// componentNodes sorts the nodes of the services the app mounts into their
// groups. A command that is not queued is no component.
func (a *App) componentNodes() componentGroups {
	var g componentGroups
	for _, n := range a.mountedNodes() {
		if job, ok := n.(*Job); ok {
			g.jobs = append(g.jobs, job)
			continue
		}
		if q, ok := n.(interface{ queued() bool }); ok && !q.queued() {
			continue
		}
		if group := g.of(n.base().kind); group != nil {
			*group = append(*group, n)
		}
	}
	return g
}

// of is the group of the nodes of kind, or nil for a kind that is no
// component.
func (g *componentGroups) of(kind model.NodeKind) *[]node {
	return map[model.NodeKind]*[]node{
		model.KindSecret: &g.secrets, model.KindStore: &g.stores, model.KindWorkflow: &g.workflows,
		model.KindMailer: &g.mailers, model.KindSubscription: &g.subs, model.KindCommand: &g.commands,
		model.KindLoop: &g.loops, model.KindListener: &g.listeners,
	}[kind]
}

// startGroups registers one lifecycle component per node, group after group.
func (a *App) startGroups(groups ...[]node) error {
	for _, group := range groups {
		for _, n := range group {
			st, ok := n.(starter)
			if !ok {
				continue // a node of this kind with nothing to bring up
			}
			b := n.base()
			if err := a.component(string(b.kind)+":"+b.id, b.id,
				func(ctx context.Context) error { return st.start(ctx, a) },
				func(ctx context.Context) error { return st.stop(ctx, a) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// component adds one lifecycle component and its Runtime view entry.
func (a *App) component(name, node string, start, stop func(context.Context) error) error {
	c := &model.Component{Name: name, Node: node, State: model.ComponentPending}
	a.mu.Lock()
	a.components = append(a.components, c)
	a.mu.Unlock()
	return a.lc.Add(lifecycle.Component{
		Name: name,
		Start: func(ctx context.Context) error {
			a.componentState(c, model.ComponentStarting, "")
			return start(ctx)
		},
		Stop: func(ctx context.Context) error {
			a.componentState(c, model.ComponentStopping, "")
			return stop(ctx)
		},
	})
}

// componentState records a lifecycle component's new state, and when a start
// or a stop of it began.
func (a *App) componentState(c *model.Component, state, errText string) {
	a.mu.Lock()
	c.State, c.Error = state, errText
	if state == model.ComponentStarting || state == model.ComponentStopping {
		c.Begun, c.TookMs = new(a.clock.Now().UTC()), 0
	}
	snapshot := *c
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLifecycle, Lifecycle: &snapshot, Phase: a.currentPhase()})
}

// onTransition receives the SDK lifecycle's report of every call it made.
func (a *App) onTransition(t lifecycle.Transition) {
	a.mu.Lock()
	var c *model.Component
	for _, x := range a.components {
		if x.Name == t.Name {
			c = x
		}
	}
	a.mu.Unlock()
	if c == nil {
		return
	}
	state := model.ComponentUp
	if t.Phase == lifecycle.PhaseStop {
		state = model.ComponentDown
	}
	errText := ""
	if t.Err != nil {
		state = model.ComponentFailed
		_, body := describe(t.Err)
		errText = body.Message
		logger.Error(context.Background(), a.log, "lifecycle component failed", logger.String("component", t.Name), logger.String("error", t.Err.Error()))
	}
	a.mu.Lock()
	c.Begun, c.TookMs = new(t.Begun.UTC()), round2(float64(t.Ended.Sub(t.Begun))/float64(time.Millisecond))
	a.mu.Unlock()
	a.componentState(c, state, errText)
}

// addScheduler registers the daemon's timers: the jobs, on one SDK
// scheduler, and each workflow's own loop, which sleeps until its next
// transition due. They start together, once the mailers and subscriptions a
// transition's hooks use are up.
func (a *App) addScheduler(jobs []*Job, workflows []node) error {
	sched := scheduler.New(scheduler.Config{Clock: a.clock, OnResult: a.onResult})
	if err := a.addJobs(sched, jobs); err != nil {
		return err
	}
	autos, states := a.timerLoops(workflows)
	r := a.newSchedRunner(sched, len(jobs))
	start := func(ctx context.Context) error {
		if err := r.start(ctx); err != nil {
			return err
		}
		for i, w := range autos {
			w.startLoop(a, states[i])
		}
		return nil
	}
	stop := func(ctx context.Context) error {
		errs := make([]error, 0, len(autos)+1)
		for _, w := range autos {
			errs = append(errs, w.stopLoop(ctx))
		}
		return errors.Join(append(errs, r.stop(ctx))...)
	}
	return a.component("scheduler", "", start, stop)
}

// addJobs hands every job to the scheduler, each with its loop.
func (a *App) addJobs(sched scheduler.Scheduler, jobs []*Job) error {
	for _, j := range jobs {
		l := a.loop(j.id, j.id, model.LoopJob, j.schedule)
		l.schedule = j.sched
		if err := sched.Add(scheduler.Entry{Name: j.id, Schedule: j.sched, Job: a.scheduled(l, j.wake(), j.run(a))}); err != nil {
			return err
		}
	}
	return nil
}

// timerLoops are the workflows that run timers of their own, each with the
// loop the Studio shows for them.
func (a *App) timerLoops(workflows []node) (autos []autoLoop, states []*loopState) {
	for _, n := range workflows {
		w, ok := n.(autoLoop)
		if !ok || !w.hasAuto() {
			continue
		}
		id := n.base().id
		autos = append(autos, w)
		states = append(states, a.loop(id+" timers", id, model.LoopTimer, autoSchedule))
	}
	return autos, states
}

// onResult receives the scheduler's report of every fire.
func (a *App) onResult(r scheduler.Result) {
	l := a.loopNamed(r.Name)
	if l == nil {
		return
	}
	a.mu.Lock()
	sched := a.rt.sched
	next := l.counted(&r)
	a.mu.Unlock()
	if sched != nil {
		sched.fired(&r, next)
	}
	switch {
	case errors.Is(r.Err, errOverlapped):
		l.skipped(a)
	case !r.Skipped:
		l.ran(a, r.Started, r.Finished, r.Err)
	}
}

// counted adds a fire's misses and skip to the loop, and says when it runs
// next, when its schedule says. The caller holds a.mu.
func (l *loopState) counted(r *scheduler.Result) *time.Time {
	l.Missed += int64(r.Missed)
	if r.Skipped {
		l.Skipped++
	}
	if l.schedule == nil {
		return nil
	}
	at, ok := l.schedule(r.Scheduled)
	if !ok {
		return nil
	}
	next := new(at.UTC())
	l.NextRun = next
	return next
}

// loop registers a loop of the internal loop. Who wrote it — a library,
// kit, the product — follows from its kind.
func (a *App) loop(name, node, kind, schedule string) *loopState {
	l := &loopState{Name: name, Node: node, Kind: kind, Schedule: schedule, State: model.LoopWaiting}
	l.Provenance, l.Library = provenanceOf(kind)
	a.mu.Lock()
	a.loops = append(a.loops, l)
	a.mu.Unlock()
	return l
}

// ran records one run of a loop and streams it.
func (l *loopState) ran(a *App, started, finished time.Time, err error) {
	a.mu.Lock()
	l.settleLocked()
	l.Runs++
	l.LastRun, l.LastMs = new(started.UTC()), round2(float64(finished.Sub(started))/float64(time.Millisecond))
	if err != nil {
		l.Errors++
		_, body := describe(err)
		l.LastError = body.Message
	}
	snapshot := l.Loop
	a.mu.Unlock()
	a.hub.publish(model.Event{Type: model.EventLoop, Loop: &snapshot})
}

// startHTTP binds kit's listener — the health probes, the Studio in dev and
// the product's HTTP — on the SDK's server: its engine accepts, bounds and
// drains the connections, net/http speaks the protocol. It returns once the
// address is bound.
func (a *App) startHTTP(ctx context.Context) error {
	newServer := plug.NewHTTPServer.Load()
	if newServer == nil {
		return failure(CodeAppListen, "LISTEN_FAILED", "the product serves HTTP without framework/kit/server", nil, errs.String("addr", a.cfg.addr))
	}
	srv := (*newServer)(plug.HTTPConfig{
		Addr: a.cfg.addr, Shards: httpShards,
		ReadHeader: httpReadHeader, Read: httpRead, Write: httpWrite, Idle: httpIdle,
		MaxHeaderBytes: httpMaxHeader, Handler: a.handler,
	})
	// In dev, the engine's goroutines — started by Start — inherit the HTTP
	// loop's label: the goroutine view and the profiles name them.
	var startErr error
	start := func(ctx context.Context) { startErr = srv.Start(ctx) }
	if a.hub != nil && a.hub.enabled {
		pprof.Do(ctx, pprof.Labels(labelLoop, "http"), start)
	} else {
		start(ctx)
	}
	if err := startErr; err != nil {
		// kit's words: the SDK's would not name the setting to change.
		return explain(CodeAppListen, "LISTEN_FAILED", "the product could not listen on its address: change KIT_ADDR", err, errs.String("addr", a.cfg.addr))
	}
	addr := cmp.Or(srv.State().Addr, a.cfg.addr)
	a.mu.Lock()
	a.server, a.addr = srv, addr
	a.mu.Unlock()
	a.instrumentHTTP()
	return nil
}

// stopHTTP drains and stops the HTTP server, if one runs.
func (a *App) stopHTTP(ctx context.Context) error {
	a.mu.Lock()
	srv := a.server
	a.mu.Unlock()
	if srv == nil {
		return nil
	}
	// The engine bounds a drain only in Serve: kit, which starts it with
	// Start, gives Shutdown its own deadline.
	ctx, cancel := context.WithTimeout(ctx, httpDrain)
	defer cancel()
	return srv.Shutdown(ctx)
}

// Stop drains the product: readiness turns false, then the components stop in
// reverse order — the HTTP listener finishes its requests before the stores
// close.
func (a *App) Stop(ctx context.Context) error {
	return a.stop(ctx, "")
}

// stop drains the product; reason says why, when it is not the caller's
// own decision: the signal the process received.
func (a *App) stop(ctx context.Context, reason string) error {
	a.mu.Lock()
	if a.phase != model.PhaseServing {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	a.mu.Lock()
	a.stopping = true
	if a.graphDirty != nil {
		a.graphDirty.Stop()
		a.graphDirty = nil
	}
	a.mu.Unlock()
	a.setPhase(model.PhaseDraining, reason)
	a.endStreams()
	a.health.Drain()
	err := a.lc.Stop(ctx)
	if a.cancel != nil {
		a.cancel()
	}
	a.wg.Wait()
	a.stopLoops()
	a.closeSecrets()
	a.unmount()
	a.setPhase(model.PhaseStopped, "")
	a.mu.Lock()
	a.server, a.addr = nil, ""
	a.mu.Unlock()
	return err
}

// Run starts the product, serves until ctx ends or the process receives
// SIGINT or SIGTERM, then drains it.
//
// Goroutine lifecycle: one goroutine turns a signal into the run's
// cancellation; it ends when a signal arrives or when the run returns, which
// cancels the context it also watches.
func (a *App) Run(ctx context.Context) error {
	signals, unsubscribe := signal.Notify(stopSignals...)
	defer unsubscribe()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case s, ok := <-signals:
			if ok {
				cancel(signalCause{sig: s})
			}
		case <-ctx.Done():
		}
	}()
	if err := a.Start(ctx); err != nil {
		return err
	}
	reason := "idle"
	select {
	case <-ctx.Done():
		reason = stopReason(ctx)
	case <-a.idleDone():
	case <-a.stopAsked():
		reason = "asked"
	}
	logger.Info(context.WithoutCancel(ctx), a.log, "draining", logger.String("reason", reason))
	stopCtx, stopCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stopCancel()
	return a.stop(stopCtx, reason)
}

// setPhase moves the process to phase p, remembers it with why, and streams
// it.
func (a *App) setPhase(p, reason string) {
	a.mu.Lock()
	a.phase = p
	a.rememberPhase(p, reason)
	a.mu.Unlock()
	a.reportPhase(p)
	if a.hub != nil {
		a.hub.publish(model.Event{Type: model.EventLifecycle, Phase: p})
	}
}

// currentPhase is the app's lifecycle phase now.
func (a *App) currentPhase() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase
}

// problem records a runtime problem: logged, and shown by the Studio.
func (a *App) problem(node string, message phrase) {
	a.mu.Lock()
	a.problems = append(a.problems, diagnosticOf("warning", node, nil, message))
	if len(a.problems) > 100 {
		a.problems = a.problems[len(a.problems)-100:]
	}
	a.mu.Unlock()
	logger.Warn(context.Background(), a.log, message.String(), logger.String("node", node))
}

// clientOf says which node a request comes from: the frontend whose page made
// it, known from the same-origin Referer, or the outside world.
func (a *App) clientOf(r *http.Request) string {
	ref := r.Header.Get("Referer")
	if ref == "" || len(a.frontends) == 0 {
		return model.ExternalID
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host != r.Host || strings.HasPrefix(u.Path, "/_kit/") {
		return model.ExternalID
	}
	best, bestLen := model.ExternalID, -1
	for _, f := range a.frontends {
		if prefix := f.served(a); strings.HasPrefix(u.Path, prefix) && len(prefix) > bestLen {
			best, bestLen = f.id, len(prefix)
		}
	}
	return best
}

// goVersion is the Go version the product was built with.
func goVersion() string { return runtime.Version() }

// sortedLoops returns the loops in registration order, copied.
func (a *App) sortedLoops() []model.Loop {
	a.countAccepts()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]model.Loop, 0, len(a.loops))
	for _, l := range a.loops {
		out = append(out, l.Loop)
	}
	return out
}

// componentsCopy returns the lifecycle components, copied.
func (a *App) componentsCopy() []model.Component {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]model.Component, 0, len(a.components))
	for _, c := range a.components {
		out = append(out, *c)
	}
	return out
}

// problemsCopy returns the runtime problems, copied.
func (a *App) problemsCopy() []model.Diagnostic {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.problems)
}

// isDiagnostics reports whether err is, or wraps, the start's refusal of the
// declarations.
func isDiagnostics(err error) bool {
	de, ok := errors.AsType[*DiagnosticsError](err)
	return ok && de != nil
}

// moduleLine is the module path a go.mod declares; "" when it declares none.
func moduleLine(gomod []byte) string {
	text := string(gomod)
	for line := range strings.Lines(text) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}
