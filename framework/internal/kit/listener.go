// Package kit — listeners: inbound ports that are not HTTP, a private socket
// speaking a versioned contract (ADR 0148).
package kit

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"path/filepath"
	"runtime/debug"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
	"github.com/kitsunium/sdk/pkg/v1/proc/ipc"
)

// ListenHandler serves one connection of a [Listener] until it returns; the
// connection is closed after it. conn.Peer says who connected, as the kernel
// says where it can (pkg/v1/proc/ipc).
type ListenHandler func(ctx context.Context, conn *ipc.Conn) error

// Listener is an inbound port that is not HTTP: a private socket on this
// machine — a Unix socket in a directory only the product's account can
// reach, the kernel naming the peer on Linux (ADR 0148) — speaking a
// versioned contract. It is how a daemon profile serves its clients, and how
// two process roles of one binary talk (D22): through the contract, never
// through each other's code.
type Listener struct {
	nodeBase
	contract string
	handler  ListenHandler
	opts     listenerOptions

	mu     sync.Mutex
	ln     *ipc.Listener
	path   string
	cancel context.CancelFunc
	open   map[*ipc.Conn]bool
	conns  sync.WaitGroup
}

// ListenerConfigurer tunes a listener.
type ListenerConfigurer interface {
	// listenerConfigure sets the option on o.
	listenerConfigure(o *listenerOptions)
}

// listenerOptions are what the options set: the socket's path, and the
// accounts admitted besides the product's own.
type listenerOptions struct {
	path                 string
	allowUIDs, allowGIDs []int
	// per are SocketPer's scopes, whose key the socket's name carries.
	per []ScopeValue
}

// listenerOption is an option as a function.
type listenerOption func(o *listenerOptions)

// listenerConfigure runs f on o.
func (f listenerOption) listenerConfigure(o *listenerOptions) { f(o) }

// SocketPath places the listener's socket at path — absolute, at most 103
// bytes — instead of <runtime dir of the app>/<service>-<name>.sock.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func SocketPath(path string) ListenerConfigurer {
	return listenerOption(func(o *listenerOptions) { o.path = path })
}

// SocketPer names the listener's socket after the value of scopes in this
// process — <runtime dir of the app>/<service>-<name>-<key>.sock, the key
// being [ScopeKey]'s —: one daemon per user, per executable, per
// configuration directory. A client finds it with SocketPathIn or DialIn,
// which compute the same key from the same declaration.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func SocketPer(scopes ...ScopeValue) ListenerConfigurer {
	return listenerOption(func(o *listenerOptions) { o.per = scopes })
}

// AllowPeers admits accounts besides the product's own: an on-call group
// declared at deployment. The kernel checks them where it names the peer; the
// socket's directory must still let them through, which is the deployment's
// to arrange.
//
// IFACE-OPAQUE: ListenerOption is sealed — its one method is unexported —
// so only this package makes one, and a caller only passes it to Listen.
func AllowPeers(uids, gids []int) ListenerConfigurer {
	return listenerOption(func(o *listenerOptions) { o.allowUIDs, o.allowGIDs = uids, gids })
}

// Listen declares a listener named name speaking contract — "<name>/v<major>",
// "render/v1" — whose connections handler serves, one goroutine each.
//
//go:noinline
func (s *Service) Listen(name, contract string, handler ListenHandler, opts ...ListenerConfigurer) *Listener {
	l := NewListener(contract, handler)
	l.kind, l.name = model.KindListener, name
	l.decl = callerPos()
	for _, o := range opts {
		if o != nil {
			o.listenerConfigure(&l.opts)
		}
	}
	if p, _ := funcInfo(handler); p.file() != "" {
		l.body = &p
	}
	s.add(l, true)
	if problem, found := l.declarationProblem(); found {
		s.problemSaid(l.decl, l.id, problem)
	}
	return l
}

// declarationProblem is the first thing wrong with the declaration; found
// is false when nothing is.
func (l *Listener) declarationProblem() (problem phrase, found bool) {
	switch {
	case l.handler == nil:
		return say("listener.nil", "name", l.name), true
	case !model.ValidContract(l.contract):
		return say("listener.contract", "name", l.name, "contract", l.contract), true
	case l.opts.path != "" && !filepath.IsAbs(l.opts.path):
		return say("listener.path", "name", l.name), true
	case l.opts.path != "" && len(l.opts.per) > 0:
		return say("listener.path-per", "name", l.name), true
	}
	return phrase{}, false
}

// describe draws the listener: a local network, its contract, and who may
// connect.
func (l *Listener) describe(a *App, out *model.Node) []model.Edge {
	peer := "owner"
	if len(l.opts.allowUIDs)+len(l.opts.allowGIDs) > 0 {
		peer = "owner+declared"
	}
	out.Listener = &model.ListenerInfo{Network: model.NetworkLocal, Contract: l.contract, Peer: peer}
	return nil
}

// SocketPathIn is where the listener's socket lies in product — the
// binary's name for a role of one, else the app's —: its SocketPath, else
// <runtime dir of product>/<service>-<name>.sock, with SocketPer's key before
// the extension. A client of another process role computes the daemon's path
// with it.
func (l *Listener) SocketPathIn(product string) (string, error) {
	if l.opts.path != "" {
		return l.opts.path, nil
	}
	return SocketPathFor(product, l.svc.name, l.name, l.opts.per...)
}

// SocketPathFor is the socket of the listener name of service in product —
// the binary's name for a role of one, else the app's —, placed by default
// and named after scopes (SocketPer): the one rule, for a client of another
// process role that does not import the daemon's declarations (D22) — it
// names the three words and the scopes its contract states. The service is
// its qualified name for a module's.
func SocketPathFor(product, service, name string, scopes ...ScopeValue) (string, error) {
	file := service + "-" + name
	if len(scopes) > 0 {
		key, err := ScopeKey(scopes...)
		if err != nil {
			return "", err
		}
		file += "-" + key
	}
	return filepath.Join(ipc.RuntimeDir(product), file+".sock"), nil
}

// DialIn connects to the listener as run in product, from another process —
// a client role of the same binary —: the path SocketPathIn computes, the
// checks Dial makes.
func (l *Listener) DialIn(ctx context.Context, product string) (*ipc.Conn, error) {
	path, err := l.SocketPathIn(product)
	if err != nil {
		return nil, err
	}
	return ipc.Dial(ctx, ipc.Config{Path: path, AllowUIDs: l.opts.allowUIDs})
}

// Path is where the listener listens while the app runs; "" otherwise.
func (l *Listener) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// Dial connects to the listener as a client of the same account would: the
// same checks, from the same configuration. A test and a sibling role use it.
func (l *Listener) Dial(ctx context.Context) (*ipc.Conn, error) {
	path := l.Path()
	if path == "" {
		return nil, unmounted(&l.nodeBase)
	}
	return ipc.Dial(ctx, ipc.Config{Path: path, AllowUIDs: l.opts.allowUIDs})
}

// start opens the socket and serves it until stop.
func (l *Listener) start(_ context.Context, a *App) error {
	path, err := l.SocketPathIn(a.product())
	if err != nil {
		return err
	}
	ln, err := ipc.Listen(ipc.Config{Path: path, AllowUIDs: l.opts.allowUIDs, AllowGIDs: l.opts.allowGIDs})
	if err != nil {
		return err
	}
	lctx, cancel := context.WithCancel(a.baseCtx)
	l.mu.Lock()
	l.ln, l.path, l.cancel, l.open = ln, path, cancel, map[*ipc.Conn]bool{}
	l.mu.Unlock()
	if !a.goTracked(func() { l.accept(lctx, a, ln) }) {
		cancel()
		closeLogged(a, l.id, ln)
		return failure(CodeAppRunning, "APP_STOPPING", "the app is stopping", nil)
	}
	return nil
}

// accept serves connections until the listener closes. Each handler runs
// with ctx, which the stop cancels; its connection is closed then too, so a
// handler blocked in a read returns.
//
// Goroutine lifecycle: one goroutine per connection, counted in l.conns; it
// ends when the handler returns, which the stop's cancel and close bring
// about, and the stop waits for l.conns.
func (l *Listener) accept(ctx context.Context, a *App, ln accepter) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errs.HasCode(err, ipc.CodeClosed) {
				logger.Warn(a.baseCtx, a.log, "a listener stopped accepting", logger.String("node", l.id), logger.String("error", errs.PublicOf(err)))
			}
			return
		}
		l.mu.Lock()
		if l.open == nil {
			l.mu.Unlock()
			closeLogged(a, l.id, conn)
			return
		}
		l.open[conn] = true
		l.conns.Add(1)
		l.mu.Unlock()
		a.activity(+1)
		go l.track(ctx, a, conn)
	}
}

// track serves conn, then forgets it.
func (l *Listener) track(ctx context.Context, a *App, conn *ipc.Conn) {
	defer l.conns.Done()
	defer a.activity(-1)
	defer func() {
		l.mu.Lock()
		if l.open != nil {
			delete(l.open, conn)
		}
		l.mu.Unlock()
	}()
	l.serve(ctx, a, conn)
}

// serve runs the handler on one connection, inside its span; a panic is the
// connection's error, never the process's.
func (l *Listener) serve(ctx context.Context, a *App, conn *ipc.Conn) {
	defer closeLogged(a, l.id, conn)
	ctx, sp := a.begin(ctx, &spanStart{node: l.id, edge: model.EdgeCalls, op: model.OpConnect, name: l.name})
	var err error
	defer func() {
		if p := recover(); p != nil {
			logger.Error(ctx, a.log, "a listener's handler panicked", logger.String("node", l.id),
				logger.Any("panic", p), logger.String("stack", string(debug.Stack())))
			err = failure(CodeHandlerPanic, "HANDLER_PANICKED", "a listener's handler panicked", nil, errs.String("listener", l.id))
		}
		sp.end(err)
	}()
	err = l.handler(withNode(ctx, l.id, a), conn)
}

// stop closes the socket and every open connection, and waits for their
// handlers until ctx ends.
//
// Goroutine lifecycle: one goroutine waits for l.conns and closes done; it
// ends once the last handler returns, which closing the connections brings
// about.
func (l *Listener) stop(ctx context.Context, a *App) error {
	l.mu.Lock()
	ln, cancel := l.ln, l.cancel
	open := slices.Collect(maps.Keys(l.open))
	l.ln, l.path, l.cancel, l.open = nil, "", nil, nil
	l.mu.Unlock()
	if ln == nil {
		return nil
	}
	err := ln.Close()
	cancel()
	for _, c := range open {
		closeLogged(a, l.id, c)
	}
	done := make(chan struct{})
	go func() {
		l.conns.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return err
}

// closeLogged closes c for the node id. Its error changes nothing — the
// connection is gone either way — so it is logged, not returned; one already
// closed is not worth a line.
func closeLogged(a *App, id string, c io.Closer) {
	if err := c.Close(); err != nil && !errs.HasCode(err, ipc.CodeClosed) && !errors.Is(err, net.ErrClosed) {
		logger.Debug(a.baseCtx, a.log, "closing a connection", logger.String("node", id), logger.String("error", errs.PublicOf(err)))
	}
}

// NewListener is a listener no service declares yet, speaking contract and
// served by handler: [Service.Listen] makes one and declares it, which is how
// a product gets one.
func NewListener(contract string, handler ListenHandler) *Listener {
	return &Listener{contract: contract, handler: handler}
}
