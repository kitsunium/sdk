package authz

// NewRequestValue builds the immutable question. Attributes are indexed by
// their key; a later attribute with the same key replaces an earlier one, so
// the caller can layer defaults and then override them.
//
// An attribute with an empty key or the zero [KindInvalid] kind is DROPPED
// rather than stored. The invariant is worth the drop: every attribute a rule
// can find is one it can use, so "found but unusable" is not a state any rule
// has to handle. The dropped attribute is then simply absent, and absence is
// already a refusal in every rule that names it — the failure mode is closed.
//
// An empty action or resource is accepted and matches no grant and no rule, so
// such a request is refused by the closure. It is not rejected here because
// doing so would put an error return on the constructor every request calls,
// to catch a mistake that already fails closed.
func NewRequestValue(subject, action, resource string, attrs ...AttrValue) RequestValue {
	//: the common request carries attributes; build the map only when it does,
	//: so an attribute-free request allocates nothing beyond the value itself.
	var indexed map[string]AttrValue
	//: one pass over the caller's slice; later duplicates overwrite earlier
	//: ones, which is what makes layering defaults then overriding them work.
	for _, attr := range attrs {
		//: drop the two shapes that could be found but not used.
		if attr.key == "" || attr.kind == KindInvalid {
			//: not storable — the attribute is absent, which rules refuse on.
			continue
		}
		//: allocate lazily, on the first attribute that survives the filter.
		if indexed == nil {
			//: size to the input; duplicate keys only over-allocate.
			indexed = make(map[string]AttrValue, len(attrs))
		}
		//: last writer wins, so callers can layer defaults then override.
		indexed[attr.key] = attr
	}
	//: assemble in one literal — every member is immutable from here on.
	return RequestValue{subject: subject, action: action, resource: resource, attrs: indexed}
}

// Subject returns the authenticated principal, or "" for anonymous.
func (r RequestValue) Subject() string {
	//: direct read of the immutable member.
	return r.subject
}

// Action returns the verb being attempted.
func (r RequestValue) Action() string {
	//: direct read of the immutable member.
	return r.action
}

// Resource returns the kind of thing being acted on.
func (r RequestValue) Resource() string {
	//: direct read of the immutable member.
	return r.resource
}

// Attr resolves an attribute by name. ok is false when the request does not
// carry it — which callers MUST NOT read as a zero-valued attribute.
//
// This two-value shape is the whole reason the attribute bag is not exported
// as a map. "The subject has no department" and "nobody told us the subject's
// department" are different facts, and a rule that cannot tell them apart
// grants access to every request that simply omitted the attribute.
func (r RequestValue) Attr(key string) (attr AttrValue, ok bool) {
	//: a nil map reads as empty, so no branch is needed for the no-attrs case.
	attr, ok = r.attrs[key]
	//: hand back both halves; the caller decides what absence means.
	return attr, ok
}

// AttrCount returns how many attributes the request carries. It exists for
// diagnostics and tests; no rule reads it, because "how many facts" never
// answers "which fact".
func (r RequestValue) AttrCount() int {
	//: len of a nil map is 0, so the no-attrs case needs no branch.
	return len(r.attrs)
}
