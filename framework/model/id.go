// The grammar of every ID a graph carries, and its parser.

package model

import (
	"github.com/kitsunium/sdk/framework/model/internal/core"
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
	SegmentPattern string = core.SegmentPattern
	// NamePattern is a node's name within its service and kind.
	NamePattern string = core.NamePattern
	// RoutePattern is the name of an endpoint declared without one: its
	// route, "GET /tasks/{id}".
	RoutePattern string = core.RoutePattern
	// ContractPattern is the versioned contract an [EdgeContracts] edge
	// carries: "render/v1".
	ContractPattern string = core.ContractPattern
	// BinaryScope prefixes the ID of a binary and of its roles.
	BinaryScope string = core.BinaryScope
	// LibraryScope prefixes the ID of a library.
	LibraryScope string = core.LibraryScope
)

// IDPattern is the whole grammar as one regular expression, anchored —
// the one the design files' JSON Schema publishes for an ID. Only an
// endpoint may be named by its route.
var IDPattern string = core.IDPattern

type (
	// ID is a node's identity, parsed: the one grammar of [IDPattern]. Its
	// String is the ID it was parsed from.
	ID = core.IDValue
)

// ValidSegment reports whether s is a legal service, module, binary, role or
// library name.
func ValidSegment(s string) bool {
	return core.ValidSegment(s)
}

// ValidName reports whether s is a legal node name. An endpoint named by its
// route is legal as an ID but is not a name a declaration may give.
func ValidName(s string) bool {
	return core.ValidName(s)
}

// ValidContract reports whether s names a versioned contract: "render/v1".
func ValidContract(s string) bool {
	return core.ValidContract(s)
}

// ParseID reads an ID. It refuses anything [IDPattern] does not match, with
// [InvalidID]; the refusal never quotes the input, which may be anything a
// caller sent.
func ParseID(s string) (ID, error) {
	return core.ParseID(s)
}

// BinaryID is the ID of a binary.
func BinaryID(binary string) string {
	return core.BinaryID(binary)
}

// RoleID is the ID of one process role of a binary.
func RoleID(binary, role string) string {
	return core.RoleID(binary, role)
}

// LibraryID is the ID of a library.
func LibraryID(library string) string {
	return core.LibraryID(library)
}
