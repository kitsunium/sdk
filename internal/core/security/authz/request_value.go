package authz

// newRequestValue is NewRequestValue's body: decl_gen.go writes NewRequestValue, from the
// design, as one call of it.
func newRequestValue(subject, action, resource string, attrs ...AttrValue) RequestValue {
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

// attr is RequestValue.Attr's body: decl_gen.go writes RequestValue.Attr, from the
// design, as one call of it.
func (r RequestValue) attr(key string) (attr AttrValue, ok bool) {
	//: a nil map reads as empty, so no branch is needed for the no-attrs case.
	attr, ok = r.attrs[key]
	//: hand back both halves; the caller decides what absence means.
	return attr, ok
}

// attrCount is RequestValue.AttrCount's body: decl_gen.go writes RequestValue.AttrCount, from the
// design, as one call of it.
func (r RequestValue) attrCount() int {
	//: len of a nil map is 0, so the no-attrs case needs no branch.
	return len(r.attrs)
}
