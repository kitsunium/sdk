package kit

import (
	"context"
	"errors"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/security/secret"
)

// Where the data keys are, against what they seal (ADR 0006 §4).

// sealingProblems judges data-key, and where kit keeps the data keys
// against the data they seal:
//
//   - KIT_DATA_KEY must hold exactly the 32 bytes of a keyring's version;
//   - a store kept on disk whose data keys would be kept in memory — kit's
//     own store of them, or data-key itself, made again at every start — is
//     refused, in every environment: its next start could not open it;
//   - a database that keeps the data keys beside a store they seal is
//     warned of: a dump of it carries both. A SQLite database never keeps
//     them — they stay in the data directory (placementOf) —, so a store
//     that seals may live beside kit.Privacy there.
func (a *App) sealingProblems() []model.Diagnostic {
	keys := a.privacyKeyStore()
	if keys == nil {
		return nil
	}
	var out []model.Diagnostic
	variable := secretVariable("KIT", dataKeySecret)
	if n, pinned := a.pinnedDataKey(); pinned && n != dataKeyBytes {
		out = append(out, diagnosticOf("error", "", nil, say("seal.data-key-size", "variable", variable, "bytes", dataKeyBytes)))
	}
	sealed := a.sealingStores()
	if len(sealed) == 0 {
		return out
	}
	first := sealed[0].base()
	kept := a.placementOf(keys)
	if kept.memory {
		out = append(out, diagnosticOf("error", first.id, a.source(&first.decl), say("seal.keys-memory", "store", first.id, "keys", keys.id)))
	}
	if a.dataKeyInMemory() {
		out = append(out, diagnosticOf("error", first.id, a.source(&first.decl),
			say("seal.data-key-memory", "store", first.id, "variable", variable)))
	}
	if kept.db == nil {
		return out
	}
	return append(out, a.keysBeside(sealed, kept.db)...)
}

// sealingStores are the product's stores that seal at rest.
func (a *App) sealingStores() []placementSource {
	var sealed []placementSource
	for _, st := range a.placedStores() {
		if s, ok := st.(sealingSource); ok && !kitOwn(st.base().svc) && s.sealsAtRest(a) {
			sealed = append(sealed, st)
		}
	}
	return sealed
}

// keysBeside warns of each store of sealed that db keeps beside the data
// keys.
func (a *App) keysBeside(sealed []placementSource, db *database) []model.Diagnostic {
	var out []model.Diagnostic
	for _, st := range sealed {
		if p := a.placementOf(st); p.db == db {
			b := st.base()
			out = append(out, diagnosticOf("warning", b.id, a.source(&b.decl), say("seal.keys-beside", "database", db.name, "store", b.id)))
		}
	}
	return out
}

// pinnedDataKey is how many bytes KIT_DATA_KEY holds, and whether it is
// set: a keyring's version is 32 bytes, no more, no less.
func (a *App) pinnedDataKey() (int, bool) {
	st := a.secrets.Load()
	if st == nil || st.kitEnv == nil {
		return 0, false
	}
	v, err := st.kitEnv.Get(context.Background(), dataKeySecret)
	if err != nil {
		return 0, false
	}
	raw := v.Value.Reveal()
	defer clear(raw)
	return len(raw), true
}

// dataKeyInMemory reports whether data-key lives in the process's memory,
// unpinned: the environment keeps its secrets there, and KIT_DATA_KEY is not
// set. Known once the start opened the secrets' stores.
func (a *App) dataKeyInMemory() bool {
	st := a.secrets.Load()
	if st == nil || st.kept == nil || st.from != model.SecretFromMemory {
		return false
	}
	_, err := st.kitEnv.Get(context.Background(), dataKeySecret)
	return errors.Is(err, secret.NotFound)
}
