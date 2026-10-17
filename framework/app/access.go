package app

import (
	iapp "github.com/kitsunium/sdk/framework/internal/app"
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
// The framework calls the caller a principal: in the framework, a subject is the person
// personal data is about (ADR 0006).
type Principal = iapp.AttrsProvider
