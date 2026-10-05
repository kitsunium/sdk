// Package kit — process profiles: what an app does with its process (ADR
// 0147 §5) — serve, run as a daemon, or run one CLI command — and the
// singleton lock.
package kit

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/lock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// minIdleStep bounds how often the idle watch looks at the listeners.
const minIdleStep time.Duration = 10 * time.Millisecond

// Process profiles: what an app does with its process (ADR 0147 §5). A
// product that is not a web server — a status line, a CLI with a daemon —
// is a kit product too.
//
//   - [model.ProfileServer], the default, serves HTTP until it is stopped.
//   - [model.ProfileDaemon] runs until it is stopped, or until it was idle
//     for its [IdleStop] time, and serves NO HTTP: its clients reach it on
//     its listeners (Service.Listen), a private socket. It has no /_kit/
//     surface and opens no TCP port.
//   - [model.ProfileCLI] parses its arguments, runs one CLI command
//     (Service.CLI) and exits with its status. It opens the stores and the
//     databases a command reads and starts nothing that outlives it: no
//     consumer, no scheduler, no loop, no listener, no HTTP.

// profileState is what the profiles keep while the app runs.
type profileState struct {
	// active counts the open connections of the listeners; last is when the
	// count last changed, in the clock's nanoseconds.
	active atomic.Int64
	last   atomic.Int64
	// idle is closed when the daemon was idle long enough; once guards it.
	idle     chan struct{}
	idleOnce sync.Once
	// lease is the singleton lock, held while the app runs.
	lease lock.Lease
	// asked is closed by Stop — the product's own request to end the run —;
	// askOnce guards it. Both are made again at every start.
	asked   chan struct{}
	askOnce *sync.Once
}

// Profile selects the app's process profile: model.ProfileServer (the
// default), model.ProfileDaemon or model.ProfileCLI.
func Profile(p string) AppConfigurer { return appOption(func(o *appOptions) { o.profile = p }) }

// IdleStop makes a daemon stop itself once, for d, no connection was open on
// any of its listeners and no activity of its services (Service.Activity)
// said it was busy: a daemon launched on demand by its clients ends when
// they stop coming. It is refused outside the daemon profile.
func IdleStop(d time.Duration) AppConfigurer { return appOption(func(o *appOptions) { o.idle = d }) }

// Singleton keeps one process of the app alive per scope on this machine: the
// start takes an exclusive file lock named after scope in the app's runtime
// directory (pkg/v1/app/lock: flock, LockFileEx) and holds it until the process
// ends. A second process refuses to start with CodeSingletonHeld — for a
// daemon's client, the sign to talk to the one that runs. The kernel drops
// the lock with a dead process.
func Singleton(scope string) AppConfigurer {
	return appOption(func(o *appOptions) { o.singleton = scope })
}

// SingletonPer keeps one process of the app alive per value of scopes on
// this machine — per user, per installed executable, per configuration
// directory ([PerUID], [PerExecutable], [PerConfigDir], [PerEnv]) —: the lock
// is named after the scopes' key ([ScopeKey]), computed at the start, and
// held as Singleton's is. A client that must find the process computes the
// same key from the same scopes.
func SingletonPer(scopes ...ScopeValue) AppConfigurer {
	return appOption(func(o *appOptions) { o.singleton, o.singletonPer = scopeLabel(scopes), scopes })
}

// Stop asks the app ctx runs in to end its run: Run returns as it does on a
// signal, its stop draining every component — a daemon told "stop" by its
// client, on its listener, ends itself so. It returns at once, before the
// stop; it reports false when ctx runs in no app — a handler's, a loop's, a
// command's context does — or the app does not run.
func Stop(ctx context.Context) bool {
	a := appOf(ctx)
	if a == nil {
		return false
	}
	a.mu.Lock()
	asked, once := a.prof.asked, a.prof.askOnce
	a.mu.Unlock()
	if asked == nil {
		return false
	}
	once.Do(func() { close(asked) })
	return true
}

// stopAsked is closed once Stop was called during this run; nil — never
// ready — before a start.
func (a *App) stopAsked() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prof.asked
}

// product names the app's runtime directory: its binary's name when it is a
// role of one — two roles of a binary share it, two products never do —, its
// own name otherwise.
func (a *App) product() string {
	if a.binary != nil {
		return a.binary.name
	}
	return a.name
}

// profile is the app's declared profile.
func (a *App) profile() string {
	if a.opts.profile == "" {
		return model.ProfileServer
	}
	return a.opts.profile
}

// runProfile is the profile of this run: the CLI profile while Main runs a CLI
// command — whatever the app declares —, its declared profile otherwise.
func (a *App) runProfile() string {
	if a.cliRun {
		return model.ProfileCLI
	}
	return a.profile()
}

// serves reports whether this run serves HTTP.
func (a *App) serves() bool { return a.runProfile() == model.ProfileServer }

// profileProblems are the declarations the profile cannot honour: an unknown
// profile, HTTP in a profile that serves none, an idle stop outside a
// daemon, a listener in a CLI run, a CLI command outside the CLI profile's
// reach — which is every profile, since Main runs it.
func (a *App) profileProblems() []model.Diagnostic {
	var out []model.Diagnostic
	p := a.profile()
	switch p {
	case model.ProfileServer, model.ProfileDaemon, model.ProfileCLI:
	default:
		return []model.Diagnostic{diagnosticOf("error", "", nil, say("profile.unknown", "profile", p))}
	}
	out = append(out, a.optionProblems(p)...)
	out = append(out, a.defaultProblems()...)
	for _, n := range a.mountedNodes() {
		if problem, found := a.nodeProfileProblem(n, p); found {
			b := n.base()
			out = append(out, diagnosticOf("error", b.id, a.source(&b.decl), problem))
		}
	}
	return out
}

// optionProblems are the app's options profile p cannot honour: an idle
// stop outside a daemon, a singleton scope outside the ID grammar.
func (a *App) optionProblems(p string) []model.Diagnostic {
	var out []model.Diagnostic
	if a.opts.idle < 0 || (a.opts.idle > 0 && p != model.ProfileDaemon) {
		out = append(out, diagnosticOf("error", "", nil, say("profile.idle", "profile", p)))
	}
	if a.opts.singleton != "" && !model.ValidSegment(a.opts.singleton) {
		out = append(out, diagnosticOf("error", "", nil, say("profile.singleton", "scope", a.opts.singleton)))
	}
	return out
}

// nodeProfileProblem is what node n declares that profile p cannot honour;
// found is false when it declares nothing of the kind.
func (a *App) nodeProfileProblem(n node, p string) (problem phrase, found bool) {
	b := n.base()
	switch {
	case b.kind == model.KindCLI && reservedCommands[b.name]:
		return say("cli.reserved", "node", b.id, "name", b.name), true
	case p == model.ProfileServer:
		return phrase{}, false
	case servesHTTP(a, n):
		return say("profile.http", "node", b.id, "profile", p), true
	case b.kind == model.KindListener && p == model.ProfileCLI:
		return say("profile.cli-listener", "node", b.id), true
	}
	return phrase{}, false
}

// servesHTTP reports whether n answers HTTP in app a: an endpoint with a
// route, a frontend, an auth.
func servesHTTP(a *App, n node) bool {
	switch n.base().kind {
	case model.KindFrontend, model.KindAuth:
		return true
	case model.KindEndpoint:
		ep, ok := n.(interface {
			servedPath(a *App) (path string, served bool)
		})
		if !ok {
			return false
		}
		_, served := ep.servedPath(a)
		return served
	default:
		return false
	}
}

// activity moves the count of open connections by delta.
func (a *App) activity(delta int64) {
	a.prof.active.Add(delta)
	a.prof.last.Store(a.clock.Now().UnixNano())
}

// idleDone is closed once the daemon was idle for its IdleStop time; nil —
// never ready — when it has none.
func (a *App) idleDone() <-chan struct{} {
	if a.opts.idle <= 0 {
		return nil
	}
	return a.prof.idle
}

// startIdle watches the listeners' activity and closes idle after d without
// a connection.
//
// Goroutine lifecycle: one tracked goroutine, which ends when the app's base
// context ends or once it closed idle; the stop waits for it.
func (a *App) startIdle(_ context.Context) error {
	d := a.opts.idle
	a.prof.idle, a.prof.idleOnce = make(chan struct{}), sync.Once{}
	a.prof.last.Store(a.clock.Now().UnixNano())
	step := max(d/4, minIdleStep)
	ok := a.goTracked(func() {
		t := a.clock.NewTicker(step)
		defer t.Stop()
		for {
			select {
			case <-a.baseCtx.Done():
				return
			case now, open := <-t.C():
				if !open {
					return
				}
				// A product busy by its own measure is active now: the idle
				// time starts again when it no longer is.
				if a.busy(a.baseCtx) {
					a.prof.last.Store(now.UnixNano())
					continue
				}
				if a.prof.active.Load() == 0 && now.Sub(time.Unix(0, a.prof.last.Load())) >= d {
					a.prof.idleOnce.Do(func() { close(a.prof.idle) })
					return
				}
			}
		}
	})
	if !ok {
		return failure(CodeAppRunning, "APP_STOPPING", "the app is stopping", nil)
	}
	return nil
}

// takeSingleton takes the app's singleton lock, or refuses the start.
func (a *App) takeSingleton(ctx context.Context) error {
	dir := filepath.Join(ipc.RuntimeDir(a.product()), "locks")
	locker, err := lock.NewFileLocker(lock.FileConfig{Dir: dir, Clock: a.clock})
	if err != nil {
		return err
	}
	name := a.opts.singleton
	if len(a.opts.singletonPer) > 0 {
		if name, err = ScopeKey(a.opts.singletonPer...); err != nil {
			return err
		}
	}
	lease, held, err := locker.TryAcquire(ctx, a.name+"."+name)
	switch {
	case err != nil:
		return err
	case !held:
		return failure(CodeSingletonHeld, "SINGLETON_HELD", "another process of the app runs", nil,
			errs.String("scope", a.opts.singleton), errs.String("dir", dir))
	}
	a.prof.lease = lease
	return nil
}

// releaseSingleton lets the lock go at the stop.
func (a *App) releaseSingleton(ctx context.Context) error {
	lease := a.prof.lease
	a.prof.lease = nil
	if lease == nil {
		return nil
	}
	return lease.Release(ctx)
}
