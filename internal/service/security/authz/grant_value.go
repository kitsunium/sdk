// Package authz — hosts GrantValue, one row of the role → permissions table.
package authz

// GrantValue binds one role to the permissions it confers. The role name is
// the application's vocabulary and the SDK attaches no meaning to it: "admin"
// is a string like any other, and nothing here believes it is special.
type GrantValue struct {
	// Role is the name the subject's roles attribute will contain.
	Role string
	// Permissions is what holding Role confers. An empty list is refused: a
	// role that grants nothing is a wiring mistake that fails closed silently.
	Permissions []PermissionValue
}
