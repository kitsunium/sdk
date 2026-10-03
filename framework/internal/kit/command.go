// Package kit — commands: an operation that changes something, with one
// handler.
package kit

import (
	"context"
	"reflect"
	"sync/atomic"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/security/authz"
)

// Command changes something: a typed input, a typed result, and one handler
// — the one declared with it. Declare it with [Service.Command]; dispatch it
// with [Command.Dispatch] from any building block, which draws a dispatches
// edge from the caller; give it a route with [Command.Expose]. A command is
// internal until it is exposed: how one service has another change
// something.
//
// A dispatch runs, in this order: the caller's user when the command asks
// for one ([Auth], implied by [Command.Allow]) — an in-process dispatch
// carries its caller's and is not authenticated again —, the input's
// validate tags, the policies ([RateLimit], [RateLimitPerClient],
// [Timeout], [Bulkhead]), the key ([Command.Key]), the transaction, the
// authorization ([Command.Allow], then [Command.Authorize]), then the
// handler. A queued command ([Queued]) is checked at its dispatch, as its
// caller — its validate tags, its authorization, its key —, and handled by
// its queue's consumer, the policies around the handling, its key then its
// transaction around the handler.
//
// A command is its unit of work (ADR 0004): its authorization and its
// handler run in one transaction ([Transact]), opened once its key is held,
// which holds its effects — a publish, a mail, a queued command's dispatch
// — until the commit. A failed command changes nothing and announces
// nothing; one dispatched inside another runs in its transaction.
// [NoTransaction] opts a command out.
type Command[C, R any] struct {
	nodeBase
	pipe pipeline[C, R]
	opts commandOptions
	access[C]
	// key names the entity a run is about (Key); keys holds it while a run
	// lasts.
	key  func(C) string
	keys keyed
	// queue is a queued command's queue while the app runs (queued.go).
	queue atomic.Pointer[commandQueue]
}

// CommandConfigurer configures a command: [Queued]; the options every operation
// takes — [Auth], [AuthOptional], [RateLimit], [RateLimitPerClient],
// [Timeout], [Bulkhead]; and, for a queued one, [MaxDeliveries] and
// [Parallelism].
type CommandConfigurer interface {
	commandConfigure(o *commandOptions)
}

type commandOptions struct {
	callOptions
	queued bool
	// noTransaction opts the command out of its transaction (NoTransaction).
	noTransaction bool
	deliveries    deliveryOptions
	// deliveriesGiven is set once MaxDeliveries or Parallelism was: a command
	// that is not queued refuses them.
	deliveriesGiven bool
}

// commandOption is a CommandOption of commands alone.
type commandOption func(o *commandOptions)

// commandConfigure sets the option on what it configures.
func (f commandOption) commandConfigure(o *commandOptions) { f(o) }

// Queued sends a command to the background. Dispatch returns once the
// command's own queue accepted it — a file queue under the data directory,
// memory without one —, and a consumer handles it, [Parallelism] at a time,
// retried after half a second up to [MaxDeliveries] attempts (5), then
// dead-lettered. It answers nothing: its result is [EmptyValue], and exposed, it
// answers 202 Accepted.
//
// Delivery is at least once: Queued is the product's promise that the
// handler tolerates a redelivery, and a [Command.Key] narrows the duplicates
// without removing them. The handler runs in the dispatcher's trace, as the
// user who dispatched it ([UserID]); what the auth handler said about that
// user is not queued. A queued command's input is a contract with the
// messages already queued: add a field, never rename one.
func Queued() CommandConfigurer {
	return commandOption(func(o *commandOptions) { o.queued = true })
}

// NoTransaction runs a command outside a transaction of its own: its
// writes land as its handler makes them, and its effects leave when made —
// a failed command may have written or announced something. On the data
// directory and in memory, where a writing command's transaction takes the
// writer turn, it lets such commands run side by side. Dispatched inside
// another command, it still runs in that one's transaction.
func NoTransaction() CommandConfigurer {
	return commandOption(func(o *commandOptions) { o.noTransaction = true })
}

// Command declares a command of the service: an operation named name, run by
// handler, that changes something. Its node is "<service>/command/<name>".
//
//	var PlaceOrder = Service.Command("place-order", placeOrder).
//		Allow(Policy, "place", "order").
//		Expose("POST /orders")
//
//go:noinline
func (s *Service) Command[C, R any](name string, handler func(context.Context, C) (R, error), opts ...CommandConfigurer) *Command[C, R] {
	c := NewCommand[C, R]()
	c.opts.deliveries = defaultDeliveries()
	for _, o := range opts {
		if o != nil {
			o.commandConfigure(&c.opts)
		}
	}
	c.kind, c.name, c.decl = model.KindCommand, name, callerPos()
	c.pipe = pipeline[C, R]{handler: handler, policies: c.opts.policies}
	if p, _ := funcInfo(handler); p.file() != "" {
		c.body = &p
	}
	s.add(c, true)
	if handler == nil {
		s.problem(c.decl, c.id, "command.nil", "command", c.id)
	}
	c.pipe.ready(s, c.decl, c.id, invalidInput(c.id))
	c.checkQueue(s)
	return c
}

// checkQueue says what is wrong with a command's queue: deliveries given to
// a command that is not queued, limits below one, a queued command that
// answers something.
func (c *Command[C, R]) checkQueue(s *Service) {
	d := c.opts.deliveries
	switch {
	case !c.opts.queued && c.opts.deliveriesGiven:
		s.problem(c.decl, c.id, "command.not-queued", "command", c.id)
	case !c.opts.queued:
	case d.maxDeliveries < 1 || d.parallelism < 1:
		s.problem(c.decl, c.id, "command.limits", "command", c.id)
	}
	if c.opts.queued && !c.pipe.noBody {
		s.problem(c.decl, c.id, "command.queued-result", "command", c.id, "type", reflect.TypeFor[R]())
	}
}

// invalidInput says that the validate tags of an operation's input cannot
// be read.
func invalidInput(id string) func(reflect.Type, string) phrase {
	return func(t reflect.Type, detail string) phrase {
		return say("operation.validate", "node", id, "type", t, "detail", detail)
	}
}

// Allow makes the command need a permission, as data: the app's policy
// must let the caller — its user, with the attributes its auth data gives
// when it implements [AttrsProvider] — do action to resource. The SDK's authz
// decides, and closes toward refusal: a role with no grant abstains, and an
// abstention is refused. Allow implies [Auth]; given twice, both
// permissions are needed. The refusal is 403 permission_denied, one
// sentence whatever the cause.
//
//	var PlaceOrder = Service.Command("place-order", placeOrder).Allow(Policy, "place", "order")
//
//go:noinline
func (c *Command[C, R]) Allow(policy authz.Policy, action, resource string) *Command[C, R] {
	c.allow(&c.nodeBase, callerPos(), policy, action, resource)
	return c
}

// Authorize gives the command a rule that needs the data — the ownership
// of what the input names, the membership of an organisation: fn sees the
// input and, through its context, the caller ([UserID], [AuthData]), and
// returns nil, [Forbidden], [NotFound] — to hide what exists — or an
// authz.Check of its own. It runs last before the handler, after
// [Command.Allow], under the command's key; its refusal is 403
// permission_denied, one sentence whatever the cause, and a NotFound or an
// Unauthenticated stays what it is. The static analysis reads fn as the
// command's code: the store it reads is the command's edge.
//
//go:noinline
func (c *Command[C, R]) Authorize(fn func(context.Context, C) error) *Command[C, R] {
	c.authorize(c.svc, callerPos(), c.id, fn)
	return c
}

// Key names the entity the command is about: two runs of the command with
// one key never overlap — the second waits, within its context's deadline
// —, runs with different keys go at once, and a queued command whose key
// waits or runs is not queued again until it is handled or dead-lettered. A
// handler that dispatches its own command with its own key is refused
// ([CodeCommandReentrant]), never a deadlock. An input whose key is empty
// is refused. The key is the span's instance: the Studio filters the runs
// of one entity.
//
// It is the SDK's lock, in the process: across a restart or between
// processes, one key can run twice — a handler stays idempotent.
//
//go:noinline
func (c *Command[C, R]) Key(fn func(C) string) *Command[C, R] {
	at := callerPos()
	c.calls = append(c.calls, builderCall{name: "Key", at: at})
	switch {
	case fn == nil:
		c.svc.problem(at, c.id, "key.nil", "command", c.id)
	case c.key != nil:
		c.svc.problem(at, c.id, "key.twice", "command", c.id)
	default:
		c.key = fn
	}
	return c
}

// Expose declares an endpoint that dispatches the command, on route —
// "METHOD /path/{param}" — with the options an endpoint takes
// ([RateLimitPerClient], [MaxBody], [Name]). It is an endpoint, named after
// the command — [Name] for a second exposure —, its request decoded as
// strictly as any, authenticated when the command asks for a user; the rest
// — validation, key, authorization — is the command's, so an exposed
// dispatch and an in-process one are checked alike. It answers 200 with the
// result, 204 for [EmptyValue], 202 for a queued command.
//
// An exposed command says who may run it: a permission ([Command.Allow]), a
// rule ([Command.Authorize]), or, when it declares neither, a marker that
// it is open on purpose — [Anyone], or [AnyUser] for any signed-in user.
// The start refuses an exposure that says none of them. A command nobody
// exposes is internal: the code that needs it dispatches it.
//
//go:noinline
func (c *Command[C, R]) Expose(route string, opts ...ExposeConfigurer) *Command[C, R] {
	expose[C, R](c.svc, callerPos(), c, route, opts)
	return c
}

// Dispatch runs the command for the node ctx runs inside — in a span on the
// command, its caller's dispatches edge —, through its whole pipeline, and
// returns its result; a queued command returns once its queue accepted it.
// The caller's user goes with it.
func (c *Command[C, R]) Dispatch(ctx context.Context, in C) (R, error) {
	a := c.app()
	if a == nil {
		var zero R
		return zero, unmounted(&c.nodeBase)
	}
	call := callSpec{app: a, node: c, edge: model.EdgeDispatches, op: model.OpDispatch, name: c.name, auth: c.authMode(), payloads: true}
	if c.opts.queued {
		from := currentNode(ctx)
		return inProcess(ctx, &call, in, func(ctx context.Context) (R, error) {
			var zero R
			return zero, c.accept(ctx, a, from, in)
		})
	}
	return inProcess(ctx, &call, in, func(ctx context.Context) (R, error) {
		key, err := c.keyFor(ctx, a, in)
		if err != nil {
			var zero R
			return zero, err
		}
		// What the command writes names it: a record's version says which
		// command made it (ADR 0007 §3).
		return c.pipe.invoke(withCommand(ctx, c.id), a, c, in, c.steps(a, key))
	})
}

// steps are a command's own, inside its policies: its key, then its
// transaction — opened once the key is held —, and in it its authorization
// and its handler.
func (c *Command[C, R]) steps(a *App, key string) steps[C] {
	if c.key == nil && !c.guarded() && c.opts.noTransaction {
		return nil
	}
	return func(ctx context.Context, in C, handle func(context.Context) error) error {
		authorized := c.transacted(a, func(ctx context.Context) error {
			if err := c.check(ctx, a, c.id, in); err != nil {
				return err
			}
			return handle(ctx)
		})
		if c.key == nil {
			return authorized(ctx)
		}
		if key == "" {
			return c.noKey()
		}
		return c.keys.run(ctx, a, c, key, authorized)
	}
}

// transacted is run in the command's transaction (ADR 0004), unless the
// command opted out.
func (c *Command[C, R]) transacted(a *App, run func(context.Context) error) func(context.Context) error {
	if c.opts.noTransaction {
		return run
	}
	return func(ctx context.Context) error { return transact(ctx, a, run) }
}

// keyFor is the key the input names, marked on the dispatch's span; "" for a
// command without one. A panic of the key function is an internal error.
func (c *Command[C, R]) keyFor(ctx context.Context, a *App, in C) (key string, err error) {
	if c.key == nil {
		return "", nil
	}
	defer func() {
		if p := recover(); p != nil {
			key, err = "", panicked(ctx, a, c.id, "key function panicked", p)
		}
	}()
	if key = c.key(in); key != "" {
		markSpan(ctx, c.id, "instance", clip(key))
	}
	return key, nil
}

// noKey refuses an input that names no key: an empty key would make every
// such dispatch wait for every other.
func (c *Command[C, R]) noKey() error {
	return Invalid(c.name + ": the input names no key")
}

// perform is a command's in-process run: Dispatch.
func (c *Command[C, R]) perform(ctx context.Context, in C) (R, error) {
	return c.Dispatch(ctx, in)
}

// edgeKind is the kind of edge a call to the Command draws.
func (c *Command[C, R]) edgeKind() model.EdgeKind { return model.EdgeDispatches }

// authMode is how the command asks for a user: a permission implies one.
func (c *Command[C, R]) authMode() string { return c.modeOf(c.opts.auth) }

// queued reports whether the command waits in a queue of its own.
func (c *Command[C, R]) queued() bool { return c.opts.queued }

// describe fills the graph node out with what the Command declares, and
// returns its edges.
func (c *Command[C, R]) describe(a *App, out *model.Node) []model.Edge {
	info := &model.CommandInfo{
		Input:       keptSchemaOf(reflect.TypeFor[C](), a != nil && c.sealsAtRest(a)),
		Result:      schemaOf(reflect.TypeFor[R]()),
		Mode:        model.ModeSync,
		Auth:        c.authMode(),
		Pipeline:    c.drawn(a),
		Permissions: c.permissions(),
		Key:         c.key != nil,
	}
	if c.ruleAt != nil && a != nil {
		info.Authorize = a.source(c.ruleAt)
	}
	if c.opts.queued {
		c.describeQueue(a, info)
	}
	out.Command = info
	return nil
}

// drawn is the command's pipeline, in the order a dispatch runs it: what a
// queued command checks at its dispatch, its queue, then the policies
// around its handling.
func (c *Command[C, R]) drawn(a *App) []model.Mechanic {
	var out []model.Mechanic
	if mode := c.authMode(); mode != "" {
		out = append(out, authMechanic(a, mode))
	}
	if c.pipe.check != nil {
		out = append(out, validateMechanic())
	}
	policies := make([]model.Mechanic, len(c.opts.policies))
	for i, p := range c.opts.policies {
		policies[i] = p.mech
	}
	if c.opts.queued {
		return append(out, c.drawnQueued(a, policies)...)
	}
	out = append(out, policies...)
	if c.key != nil {
		out = append(out, keyMechanic("one run at a time per key"))
	}
	out = append(out, c.transactionDrawn()...)
	return append(out, c.mechanics()...)
}

// drawnQueued is a queued command's pipeline after its authentication and
// validation: what it checks at its dispatch, its queue, then the policies
// around its handling.
func (c *Command[C, R]) drawnQueued(a *App, policies []model.Mechanic) []model.Mechanic {
	out := c.mechanics()
	if c.key != nil {
		out = append(out, keyMechanic("not queued again while one waits or runs"))
	}
	out = append(append(out, c.queueMechanic(a)), policies...)
	return append(out, c.transactionDrawn()...)
}

// transactionDrawn is the command's transaction, none with NoTransaction.
func (c *Command[C, R]) transactionDrawn() []model.Mechanic {
	if c.opts.noTransaction {
		return nil
	}
	return []model.Mechanic{transactionMechanic()}
}

// transactionMechanic is a command's transaction: its writes commit
// together, and its effects leave at the commit.
func transactionMechanic() model.Mechanic {
	return model.Mechanic{Kind: "transaction", Label: "one transaction · effects at the commit", Package: "github.com/kitsunium/sdk/pkg/v1/data/sql"}
}

// keyMechanic is the key's step: the SDK's lock, in the process.
func keyMechanic(label string) model.Mechanic {
	return model.Mechanic{Kind: "key", Label: label, Package: "github.com/kitsunium/sdk/pkg/v1/lock", Config: map[string]string{"scope": "process"}}
}

// NewCommand is a command no service declares yet: [Service.Command] makes
// one and declares it, which is how a product gets one.
func NewCommand[C, R any]() *Command[C, R] { return &Command[C, R]{} }
