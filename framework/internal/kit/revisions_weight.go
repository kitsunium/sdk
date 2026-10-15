package kit

import (
	"context"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// What a store's versions weigh (ADR 0007 §5): the Studio shows it per
// store, on a running product. The SDK's document store says it without
// reading a version (docstore.VersionWeigher, ADR 0143): in memory and in
// the data directory a counter every write moves, on a database one
// aggregate statement over the versions' table. A sealing store's versions
// weigh what they rest as — sealed.

// versionWeighter is an engine that says what its records' versions weigh:
// the SDK's document store opened with Versions, and the engines that wrap
// one. A sibling of the port (store_engine.go).
type versionWeighter interface {
	versionWeight(ctx context.Context) (docstore.VersionWeight, error)
}

// versionWeight is what the document store's versions weigh, from the
// counter its writes move.
//
//ktn:allow-unused-param: the versionWeighter port passes a context the local document store does not take
func (e *docEngine[T]) versionWeight(_ context.Context) (docstore.VersionWeight, error) {
	return e.ds.VersionWeight()
}

// versionWeight is what the table's versions weigh, in one statement on the
// transaction ctx carries for the database, else on the pool, within the
// database's timeout.
func (e *sqlEngine[T]) versionWeight(ctx context.Context) (docstore.VersionWeight, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return docstore.VersionWeight{}, err
	}
	defer done()
	return e.ds.VersionWeight(c)
}

// versionWeight is what the versions of the engine a history wraps weigh.
func (h *historied[T]) versionWeight(ctx context.Context) (docstore.VersionWeight, error) {
	return engineVersionWeight(ctx, h.storeEngine)
}

// versionWeight is what a sealing store's versions weigh as they rest.
func (e *sealedEngine[T]) versionWeight(ctx context.Context) (docstore.VersionWeight, error) {
	return engineVersionWeight(ctx, e.inner)
}

// engineVersionWeight is what eng's versions weigh, or VersionsNotKept for
// an engine that keeps none.
func engineVersionWeight[T any](ctx context.Context, eng storeEngine[T]) (docstore.VersionWeight, error) {
	if vw, ok := eng.(versionWeighter); ok {
		return vw.versionWeight(ctx)
	}
	return docstore.VersionWeight{}, docstore.VersionsNotKept
}

// weighVersions sets on info what the store's versions weigh, on a running
// product, when the store keeps revisions and its engine says: nothing when
// it cannot — the Studio then shows how many versions a store keeps, as
// before.
func (s *StoreService[T]) weighVersions(a *App, info *model.StoreHistory) {
	if s.revisions == 0 || a == nil || !a.running() {
		return
	}
	eng := s.engine()
	if eng == nil {
		return
	}
	w, err := engineVersionWeight(context.Background(), eng)
	if err != nil {
		return
	}
	info.Versions, info.Trashed, info.VersionBytes = &w.Former, &w.Trashed, &w.Bytes
}
