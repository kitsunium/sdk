// Package kit — who may call an operation: the principal, the rules and their
// checks.
package kit

import (
	"context"
	"net/http"
	"reflect"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/authz"
	"github.com/kitsunium/sdk/pkg/v1/logger"
)

// Who may dispatch a command or ask a query: the permissions Allow declares,
// checked by the SDK's authz, and the rule Authorize gives, which needs the
// data. Both run last before the handler — a command's inside its key —, so
// what they read is what the handler changes. Both may be declared; both
// must pass.

// AttrsProvider is what the app's auth data says about the caller to the
// SDK's authz: the attributes [Command.Allow] and [Query.Allow] hand a
// policy — the caller's roles, its groups. Auth data that does not
// implement it gives none, and every Allow refuses: the start warns of it.
//
//	type Me struct{ Roles []string }
//
//	func (m Me) Attrs() []authz.Attr { return []authz.Attr{authz.AttrStrings("roles", m.Roles...)} }
//
// kit calls the caller a principal: in kit, a subject is the person
// personal data is about (ADR 0006).
type AttrsProvider interface {
	Attrs() []authz.Attr
}

// permitted is one permission Allow declares, with the policy that grants
// it.
type permitted struct {
	policy authz.Policy
	perm   model.Permission
}

// access is who may run an operation: its permissions (Allow) and its rule
// (Authorize).
type access[Req any] struct {
	allowed []permitted
	// rule is Authorize's function, ruleAt its body.
	rule   func(context.Context, Req) error
	ruleAt *pos
	// calls are the builders called on the operation, where: a module's
	// operations are built from its own Go module only (ADR 0008).
	calls []builderCall
}

// builderCall is one builder called on an operation — Allow, Authorize,
// Key — and where it was called.
type builderCall struct {
	name string
	at   pos
}

// builders are the builders called on the operation, in order.
func (x *access[Req]) builders() []builderCall { return x.calls }

// allow records a permission of the operation n; a problem is said on it.
func (x *access[Req]) allow(n *nodeBase, at pos, policy authz.Policy, action, resource string) {
	s, id := n.svc, n.id
	x.calls = append(x.calls, builderCall{name: "Allow", at: at})
	switch {
	case policy == nil:
		s.problem(at, id, "allow.nil-policy", "node", id)
		return
	case strings.TrimSpace(action) == "" || strings.TrimSpace(resource) == "":
		s.problem(at, id, "allow.empty", "node", id)
		return
	}
	x.allowed = append(x.allowed, permitted{policy: policy, perm: model.Permission{Action: action, Resource: resource}})
}

// authorize records the rule; a problem is said on the operation.
func (x *access[Req]) authorize(s *Service, at pos, id string, fn func(context.Context, Req) error) {
	x.calls = append(x.calls, builderCall{name: "Authorize", at: at})
	switch {
	case fn == nil:
		s.problem(at, id, "authorize.nil", "node", id)
		return
	case x.rule != nil:
		s.problem(at, id, "authorize.twice", "node", id)
		return
	}
	x.rule = fn
	if p, _ := funcInfo(fn); p.file != "" {
		x.ruleAt = &p
	}
}

// guarded reports whether the operation declares who may run it.
func (x *access[Req]) guarded() bool { return len(x.allowed) > 0 || x.rule != nil }

// allows reports whether the operation needs a permission.
func (x *access[Req]) allows() bool { return len(x.allowed) > 0 }

// modeOf is how the operation asks for a user, declared asking so: a
// permission implies one.
func (x *access[Req]) modeOf(declared string) string {
	if len(x.allowed) > 0 {
		return model.AuthRequired
	}
	return declared
}

// check lets the caller through, or answers the refusal: every permission
// first, then the rule.
func (x *access[Req]) check(ctx context.Context, a *App, id string, req Req) error {
	for _, p := range x.allowed {
		if err := authz.Check(ctx, p.policy, requestOf(ctx, p.perm)); err != nil {
			return refusal(ctx, a, id, err)
		}
	}
	if x.rule != nil {
		if err := x.rule(ctx, req); err != nil {
			return refusal(ctx, a, id, err)
		}
	}
	return nil
}

// requestOf is the question a permission asks the SDK's authz: may the
// caller — its user ID, the attributes its auth data gives — do the action
// to the resource.
func requestOf(ctx context.Context, perm model.Permission) authz.Request {
	var attrs []authz.Attr
	if p, ok := ctx.Value(authKey{}).(principal); ok {
		if pr, ok := p.data.(AttrsProvider); ok {
			attrs = pr.Attrs()
		}
	}
	uid, _ := UserID(ctx)
	return authz.NewRequest(string(uid), perm.Action, perm.Resource, attrs...)
}

// denied is the one refusal of an authorization: the other side must not
// tell a denial from an abstention or a fault.
func denied() *Error {
	return NewError(http.StatusForbidden, WireForbidden, "access to the requested resource is denied")
}

// refusal is what a refused authorization answers: the one 403, whatever
// the cause — but a NotFound, with which a rule hides what exists, and an
// Unauthenticated stay what they are. A fault is logged, never told.
func refusal(ctx context.Context, a *App, id string, err error) error {
	status, _ := describe(err)
	switch {
	case status == http.StatusNotFound || status == http.StatusUnauthorized:
		return err
	case status >= http.StatusInternalServerError && a != nil:
		logger.Error(ctx, a.log, "authorization failed", logger.String("node", id), logger.String("error", err.Error()))
	}
	return denied()
}

// mechanics are the pipeline's authorize steps: one per permission — the
// SDK's authz — then the rule, pointing at its function.
func (x *access[Req]) mechanics() []model.Mechanic {
	var out []model.Mechanic
	for _, p := range x.allowed {
		out = append(out, model.Mechanic{
			Kind: "authorize", Label: p.perm.Action + " " + p.perm.Resource, Package: "github.com/kitsunium/sdk/pkg/v1/authz",
			Config: map[string]string{"action": p.perm.Action, "resource": p.perm.Resource},
		})
	}
	if x.rule != nil {
		m := model.Mechanic{Kind: "authorize", Label: "Authorize"}
		if x.ruleAt != nil {
			m.Label, m.Config = shortFunc(x.ruleAt.fn), map[string]string{"func": x.ruleAt.fn}
		}
		out = append(out, m)
	}
	return out
}

// permissions are the operation's permissions, for its node.
func (x *access[Req]) permissions() []model.Permission {
	var out []model.Permission
	for _, p := range x.allowed {
		out = append(out, p.perm)
	}
	return out
}

// principalType is kit.Principal's type, which auth data implements to give
// Allow its attributes.
var principalType = reflect.TypeFor[AttrsProvider]()
