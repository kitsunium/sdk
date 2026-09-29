// The grammar of every ID a graph carries, and its parser.

package core

import (
	"slices"
	"strings"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The grammar of every ID a graph carries, written once. The runtime, the
// analyzer, the design files' JSON Schema and the Studio all read it from
// here: a second copy is how two sides come to derive different names for the
// same declaration (invariant 1 of the platform's kit).
//
//	id       = "external"
//	         | service
//	         | service "/" kind "/" name
//	         | "binary:" segment
//	         | "binary:" segment "/role/" segment
//	         | "library:" segment
//	service  = [ segment "." ] segment        ; "<module>.<service>"
//	segment  = [a-z][a-z0-9-]{0,62}
//	name     = [A-Za-z][A-Za-z0-9_.-]{0,62}
//	         | method " " path                 ; an endpoint named by its route
//
// A module's service named like the module is the module's name alone
// ([QualifiedService]); every other service of a module is qualified.
const (
	// SegmentPattern is a service, module, binary, role or library name.
	SegmentPattern = `[a-z][a-z0-9-]{0,62}`
	// NamePattern is a node's name within its service and kind.
	NamePattern = `[A-Za-z][A-Za-z0-9_.-]{0,62}`
	// RoutePattern is the name of an endpoint declared without one: its
	// route, "GET /tasks/{id}".
	RoutePattern = `[A-Z]+ /[^\s]*`
	// ContractPattern is the versioned contract an [EdgeContracts] edge
	// carries: "render/v1".
	ContractPattern = `[a-z][a-z0-9-]{0,62}/v[1-9][0-9]{0,3}`

	// BinaryScope prefixes the ID of a binary and of its roles.
	BinaryScope = "binary:"
	// LibraryScope prefixes the ID of a library.
	LibraryScope = "library:"
)

var (
	// IDPattern is the whole grammar as one regular expression, anchored —
	// the one the design files' JSON Schema publishes for an ID. Only an
	// endpoint may be named by its route.
	IDPattern string = `^(?:external` +
		`|(?:` + SegmentPattern + `\.)?` + SegmentPattern +
		`(?:/(?:` + kindAlternatives() + `)/` + NamePattern + `|/endpoint/` + RoutePattern + `)?` +
		`|binary:` + SegmentPattern + `(?:/role/` + SegmentPattern + `)?` +
		`|library:` + SegmentPattern + `)$`

	// serviceKinds are the kinds a service holds, so the only ones a
	// "<service>/<kind>/<name>" ID may name.
	serviceKinds = map[NodeKind]bool{
		KindEndpoint: true, KindStore: true, KindTopic: true, KindSubscription: true,
		KindWorkflow: true, KindJob: true, KindFrontend: true, KindAuth: true,
		KindMailer: true, KindLoop: true, KindSecret: true, KindPort: true,
		KindCommand: true, KindQuery: true, KindCLI: true, KindListener: true,
		KindPresentation: true,
	}
)

// IDValue is a node's identity, parsed: the one grammar of [IDPattern]. Its
// String is the ID it was parsed from.
type IDValue struct {
	// Module qualifies Service when a module lists it; empty for the
	// product's own services, and for a module's service named like it.
	Module string
	// Service is the service's own name, unqualified; empty for a binary, a
	// role, a library and [ExternalID].
	Service string
	// Scope is the name of the binary of a binary or a role, or of the
	// library of a library; empty otherwise.
	Scope string
	// Kind is the node's kind.
	Kind NodeKind
	// Name is the node's name: the service's for a service, the binary's for
	// a binary.
	Name string
}

// kindAlternatives is the service kinds as a regular-expression alternation,
// sorted so the pattern is the same string on every run.
func kindAlternatives() string {
	kinds := make([]string, 0, len(serviceKinds))
	for k := range serviceKinds {
		kinds = append(kinds, string(k))
	}
	slices.Sort(kinds)
	return strings.Join(kinds, "|")
}

// ValidSegment reports whether s is a legal service, module, binary, role or
// library name.
func ValidSegment(s string) bool { return wordOf(s, isLower, isSegmentByte) }

// ValidName reports whether s is a legal node name. An endpoint named by its
// route is legal as an ID but is not a name a declaration may give.
func ValidName(s string) bool { return wordOf(s, isLetter, isNameByte) }

// ValidContract reports whether s names a versioned contract: "render/v1".
func ValidContract(s string) bool {
	name, version, found := strings.Cut(s, "/v")
	return found && ValidSegment(name) && len(version) <= maxVersionDigits &&
		version != "" && version[0] != '0' && strings.IndexFunc(version, notDigit) < 0
}

// ParseID reads an ID. It refuses anything [IDPattern] does not match, with
// [InvalidID]; the refusal never quotes the input, which may be anything a
// caller sent.
func ParseID(s string) (IDValue, error) {
	if id, scoped, err := parseScoped(s); scoped {
		return id, err
	}
	return parseNodeID(s)
}

// parseScoped reads the IDs outside every service — the outside world, a
// binary or one of its roles, a library —; scoped is false for any other
// string.
func parseScoped(s string) (id IDValue, scoped bool, err error) {
	switch {
	case s == ExternalID:
		return IDValue{Kind: KindExternal, Name: ExternalID}, true, nil
	case strings.HasPrefix(s, BinaryScope):
		id, err = parseBinary(strings.TrimPrefix(s, BinaryScope))
		return id, true, err
	case strings.HasPrefix(s, LibraryScope):
		name := strings.TrimPrefix(s, LibraryScope)
		if !ValidSegment(name) {
			return IDValue{}, true, invalidID("library name")
		}
		return IDValue{Scope: name, Kind: KindLibrary, Name: name}, true, nil
	default:
		return IDValue{}, false, nil
	}
}

// parseNodeID reads a service's ID, or one of its nodes':
// "<service>/<kind>/<name>".
func parseNodeID(s string) (IDValue, error) {
	service, rest, nested := strings.Cut(s, "/")
	module, name, err := parseService(service)
	if err != nil {
		return IDValue{}, err
	}
	if !nested {
		return IDValue{Module: module, Service: name, Kind: KindService, Name: service}, nil
	}
	kind, node, ok := strings.Cut(rest, "/")
	nodeKind := NodeKind(kind)
	if !ok || !serviceKinds[nodeKind] {
		return IDValue{}, invalidID("kind")
	}
	if !ValidName(node) && (nodeKind != KindEndpoint || !validRoute(node)) {
		return IDValue{}, invalidID("node name")
	}
	return IDValue{Module: module, Service: name, Kind: nodeKind, Name: node}, nil
}

// parseBinary reads what follows "binary:": a binary, or one of its roles.
func parseBinary(rest string) (IDValue, error) {
	binary, role, isRole := strings.Cut(rest, "/role/")
	if !ValidSegment(binary) {
		return IDValue{}, invalidID("binary name")
	}
	if !isRole {
		return IDValue{Scope: binary, Kind: KindBinary, Name: binary}, nil
	}
	if !ValidSegment(role) {
		return IDValue{}, invalidID("role name")
	}
	return IDValue{Scope: binary, Kind: KindRole, Name: role}, nil
}

// parseService splits "<module>.<service>" and checks both halves.
func parseService(s string) (module, service string, err error) {
	module, service, qualified := strings.Cut(s, ".")
	if !qualified {
		module, service = "", s
	}
	if !ValidSegment(service) || (qualified && !ValidSegment(module)) {
		return "", "", invalidID("service name")
	}
	return module, service, nil
}

// QualifiedServiceName is the service's ID segment: the qualification
// [QualifiedService] applies.
func (id *IDValue) QualifiedServiceName() string {
	if id.Service == "" {
		return ""
	}
	if id.Module == "" {
		return id.Service
	}
	return id.Module + "." + id.Service
}

// String renders the ID; ParseID(id.String()) is id for every ID ParseID
// returned.
func (id *IDValue) String() string {
	switch id.Kind {
	case KindExternal:
		return ExternalID
	case KindBinary:
		return BinaryScope + id.Scope
	case KindRole:
		return BinaryScope + id.Scope + "/role/" + id.Name
	case KindLibrary:
		return LibraryScope + id.Scope
	case KindService:
		return id.QualifiedServiceName()
	default:
		return NodeID(id.QualifiedServiceName(), id.Kind, id.Name)
	}
}

// BinaryID is the ID of a binary.
func BinaryID(binary string) string { return BinaryScope + binary }

// RoleID is the ID of one process role of a binary.
func RoleID(binary, role string) string { return BinaryScope + binary + "/role/" + role }

// LibraryID is the ID of a library.
func LibraryID(library string) string { return LibraryScope + library }

// invalidID is the refusal of ParseID, naming the part that failed — never
// the input.
func invalidID(part string) error {
	return errs.Wrap(InvalidID, errs.WrapParams{}, errs.String("part", part))
}
