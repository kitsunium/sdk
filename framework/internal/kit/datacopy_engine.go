package kit

import "github.com/kitsunium/sdk/framework/internal/kit/plug"

// The document stores a data copy reads and writes through (ADR 0178 C7):
// the store subsystem's file engine under the data directory and its SQL
// engine on the database (plug.DocumentSource), each reached through what the
// history and the sealing put in front of it.

// documentsOf is the document store eng runs on, through what wraps it.
func documentsOf(eng plug.StoreEngine) (copyDocs, bool) {
	d, ok := eng.(plug.DocumentSource)
	if !ok {
		return nil, false
	}
	return d.Documents()
}

// Documents is the store's engine behind the history.
func (h *historied) Documents() (plug.Documents, bool) { return documentsOf(h.StoreEngine) }

// Documents is the store's document store behind the sealing.
func (e *sealedEngine) Documents() (plug.Documents, bool) { return documentsOf(e.inner) }
