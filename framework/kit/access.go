// Package kit — who may call an operation: the principal, the rules and their
// checks.
package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
)

// Principal is what the app's auth data says about the caller to the
// SDK's authz: the attributes [Command].Allow and [Query].Allow hand a
// policy — the caller's roles, its groups. Auth data that does not
// implement it gives none, and every Allow refuses: the start warns of it.
//
//	type Me struct{ Roles []string }
//
//	func (m Me) Attrs() []authz.Attr { return []authz.Attr{authz.AttrStrings("roles", m.Roles...)} }
//
// kit calls the caller a principal: in kit, a subject is the person
// personal data is about (ADR 0006).
type Principal = ikit.AttrsProvider
