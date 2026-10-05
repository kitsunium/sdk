// What a store remembers: its history, the former values of a field, and a
// record's past.

package core

// History (ADR 0007): what a store remembers of its records — the former
// values of the fields tagged history=N, and the password policies that
// read them — and a field's former values, as Store.Former and kit.Export
// give them.

// sources are the declarations a store's history names — its password
// policies —, for Files.
func (h *StoreHistoryMessage) sources() []*SourceMessage {
	if h == nil {
		return nil
	}
	out := make([]*SourceMessage, 0, len(h.Passwords))
	for i := range h.Passwords {
		out = append(out, h.Passwords[i].Source)
	}
	return out
}

// structure is what a store's history says of the product's structure, for
// the revision: without what a run counts — kept, bytes.
func (h *StoreHistoryMessage) structure() *StoreHistoryMessage {
	if h == nil {
		return nil
	}
	cp := *h
	cp.Kept, cp.Bytes = nil, nil
	return &cp
}
