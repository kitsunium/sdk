package kit

import (
	"slices"

	"github.com/kitsunium/sdk/framework/model"
)

// diagnostic is a declaration problem, kept with its position until the App
// can relativize it.
type diagnostic struct {
	severity string
	message  phrase
	node     string
	at       pos
}

// NewService declares a service. name must be lower-case letters, digits and
// dashes, starting with a letter; doc is one sentence saying what it owns.
//
//go:noinline
func NewService(name, doc string) *Service {
	s := &Service{name: name, doc: doc, decl: callerPos(), ids: map[string]bool{}}
	if !model.ValidSegment(name) {
		s.problem(s.decl, name, "service.name", "name", name)
	}
	return s
}

// Name returns the service name, which is also its node ID: qualified with
// its module's, "moderation.intake", when a module lists it.
func (s *Service) Name() string { return s.name }

// add registers a building block.
func (s *Service) add(n node, validName bool) {
	b := n.base()
	b.svc = s
	b.id = model.NodeID(s.name, b.kind, b.name)
	s.mu.Lock()
	defer s.mu.Unlock()
	if validName && !model.ValidName(b.name) {
		s.diags = append(s.diags, diagnostic{
			severity: "error", node: b.id, at: b.decl,
			message: say("node.name", "kind", b.kind, "name", b.name),
		})
	}
	if s.ids[b.id] {
		s.diags = append(s.diags, diagnostic{
			severity: "error", node: b.id, at: b.decl,
			message: say("node.duplicate", "service", s.name, "kind", b.kind, "name", b.name),
		})
		return
	}
	s.ids[b.id] = true
	s.nodes = append(s.nodes, n)
}

// problem records a declaration error: the catalogues' key with its
// arguments, as name, value pairs (words.go).
func (s *Service) problem(at pos, node, key string, args ...any) {
	s.problemSaid(at, node, say(key, args...))
}

// problemSaid records a declaration error already said.
func (s *Service) problemSaid(at pos, node string, p phrase) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diags = append(s.diags, diagnostic{severity: "error", node: node, at: at, message: p})
}

// warn records a declaration warning.
func (s *Service) warn(at pos, node, key string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diags = append(s.diags, diagnostic{severity: "warning", node: node, at: at, message: say(key, args...)})
}

// snapshot returns the service's nodes and diagnostics.
func (s *Service) snapshot() ([]node, []diagnostic) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.nodes), slices.Clone(s.diags)
}
