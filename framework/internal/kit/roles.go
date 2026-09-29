// Package kit — binaries and their process roles: one executable, several
// Apps, and the versioned contracts between them (D22).
package kit

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
)

// exitConfig is sysexits' EX_CONFIG: the binary is declared wrong.
const exitConfig int = 78

// exitUsage is sysexits' EX_USAGE: the arguments select no role.
const exitUsage int = 64

// Binary is one executable the product ships, made of process roles: a
// status line and its daemon are two roles of one binary, "statusline" and
// "statusline daemon". Each role is an App — its own services, its own
// profile — and two roles talk only through a declared, versioned contract
// (D22): a listener one serves and the other dials, never each other's code.
//
//	var Binary = kit.NewBinary("statusline", "The status line of a Claude session.").
//		Role("render", RenderApp).                // statusline ...
//		Role("daemon", DaemonApp, "daemon").      // statusline daemon ...
//		Talks("render", "daemon", "render/v1")
//
//	func main() { os.Exit(Binary.Main(context.Background(), os.Args[1:])) }
//
// The package that declares a binary and its roles is wiring, generated from
// the design (D22).
type Binary struct {
	name, doc string
	decl      pos
	roles     []*role
	contracts []contract
	problems  []phrase
}

// role is one process role of a binary.
type role struct {
	name string
	app  *App
	args []string
	decl pos
}

// contract is a declared edge between two roles.
type contract struct{ from, to, name string }

// NewBinary declares the binary named name.
//
//go:noinline
func NewBinary(name, doc string) *Binary {
	b := &Binary{name: name, doc: doc, decl: callerPos()}
	if !model.ValidSegment(name) {
		b.problems = append(b.problems, say("binary.name", "name", name))
	}
	return b
}

// Role adds a process role named name, run by app, selected when the
// process's first arguments are args; the role without args is the default.
// It returns b, so roles chain.
func (b *Binary) Role(name string, app *App, args ...string) *Binary {
	switch {
	case !model.ValidSegment(name):
		b.problems = append(b.problems, say("binary.role-name", "binary", b.name, "name", name))
	case app == nil:
		b.problems = append(b.problems, say("binary.role-nil", "binary", b.name, "role", name))
	case slices.ContainsFunc(b.roles, func(r *role) bool { return r.name == name || slices.Equal(r.args, args) }):
		b.problems = append(b.problems, say("binary.role-twice", "binary", b.name, "role", name))
	default:
		app.binary, app.role = b, name
	}
	b.roles = append(b.roles, &role{name: name, app: app, args: slices.Clone(args), decl: callerPos()})
	return b
}

// Talks declares that role from talks to role to through contract —
// "<name>/v<major>". It is the only edge two roles may have.
func (b *Binary) Talks(from, to, contractName string) *Binary {
	if !model.ValidContract(contractName) || b.roleNamed(from) == nil || b.roleNamed(to) == nil || from == to {
		b.problems = append(b.problems, say("binary.contract", "binary", b.name, "from", from, "to", to, "contract", contractName))
	}
	b.contracts = append(b.contracts, contract{from: from, to: to, name: contractName})
	return b
}

// roleNamed is b's role named name, or nil.
func (b *Binary) roleNamed(name string) *role {
	for _, r := range b.roles {
		if r.name == name {
			return r
		}
	}
	return nil
}

// Main runs the role the arguments select — the one whose args are the
// longest prefix of them, else the default role — with the rest of the
// arguments, through its App's Main. It returns the process exit status.
func (b *Binary) Main(ctx context.Context, args []string) int {
	if len(b.problems) > 0 {
		for _, p := range b.problems {
			if _, err := os.Stderr.WriteString(p.String() + "\n"); err != nil {
				break
			}
		}
		return exitConfig
	}
	r, rest := b.selectRole(args)
	if r == nil {
		if _, err := os.Stderr.WriteString(b.name + ": no default role: say which, one of " + strings.Join(b.roleWords(), ", ") + "\n"); err != nil {
			return exitUsage
		}
		return exitUsage
	}
	return r.app.Main(ctx, rest)
}

// selectRole is the role args select and the arguments left for it.
func (b *Binary) selectRole(args []string) (*role, []string) {
	var best *role
	for _, r := range b.roles {
		if len(r.args) <= len(args) && slices.Equal(r.args, args[:len(r.args)]) && (best == nil || len(r.args) > len(best.args)) {
			best = r
		}
	}
	if best == nil {
		return nil, args
	}
	return best, args[len(best.args):]
}

// roleWords are the command lines that select each role, for a usage line.
func (b *Binary) roleWords() []string {
	var out []string
	for _, r := range b.roles {
		out = append(out, strings.Join(append([]string{b.name}, r.args...), " "))
	}
	return out
}

// describeBinary adds, to the graph of the app of one of its roles, the
// binary, every role of it — the others are other processes, drawn so the
// contracts between them have ends — and the edges: each contract, and what
// this role runs (its listeners and its CLI commands).
func (a *App) describeBinary(g *model.Graph) []model.Edge {
	b := a.binary
	if b == nil {
		return nil
	}
	doc, docs := model.SplitDoc(b.doc)
	binaryID := model.BinaryID(b.name)
	info := &model.BinaryInfo{Roles: []string{}}
	for _, r := range b.roles {
		info.Roles = append(info.Roles, model.RoleID(b.name, r.name))
	}
	g.Nodes = append(g.Nodes, model.Node{
		ID: binaryID, Kind: model.KindBinary, Name: b.name, Doc: doc, Docs: docs,
		Source: a.source(&b.decl), Binary: info,
	})
	for _, r := range b.roles {
		g.Nodes = append(g.Nodes, model.Node{
			ID: model.RoleID(b.name, r.name), Kind: model.KindRole, Name: r.name,
			Source: a.source(&r.decl), Role: roleInfo(binaryID, r),
		})
	}
	var edges []model.Edge
	for _, c := range b.contracts {
		edges = append(edges, model.Edge{
			From: model.RoleID(b.name, c.from), To: model.RoleID(b.name, c.to),
			Kind: model.EdgeContracts, Contract: c.name, Declared: true,
		})
	}
	self := model.RoleID(b.name, a.role)
	for _, n := range a.mountedNodes() {
		if k := n.base().kind; k == model.KindListener || k == model.KindCLI {
			edges = append(edges, model.Edge{From: self, To: n.base().id, Kind: model.EdgeRuns, Declared: true})
		}
	}
	return edges
}

// roleInfo is what the graph says of role r of the binary binaryID: its
// profile, its arguments, its singleton scope and idle stop.
func roleInfo(binaryID string, r *role) *model.RoleInfo {
	if r.app == nil {
		return &model.RoleInfo{Binary: binaryID, Profile: model.ProfileServer, Args: r.args}
	}
	return &model.RoleInfo{
		Binary: binaryID, Profile: r.app.profile(), Args: r.args,
		Singleton: r.app.opts.singleton, IdleMs: r.app.opts.idle.Milliseconds(),
	}
}
