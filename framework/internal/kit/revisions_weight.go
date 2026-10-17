package kit

import (
	"context"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
)

// What a store's versions weigh (ADR 0007 §5): the Studio shows it per
// store, on a running product. The SDK's document store says it without
// reading a version (docstore.VersionWeigher, ADR 0143): in memory and in
// the data directory a counter every write moves, on a database one
// aggregate statement over the versions' table. A sealing store's versions
// weigh what they rest as — sealed.

// pluggedWeight is the SDK's weight of a store's versions as its engine
// hands it on.
func pluggedWeight(w docstore.VersionWeight, err error) (plug.VersionWeightValue, error) {
	return plug.VersionWeightValue{Former: w.Former, Trashed: w.Trashed, Bytes: w.Bytes}, err
}

// VersionWeight is what the document store's versions weigh, from the
// counter its writes move.
//
//ktn:allow-unused-param: the VersionWeighter interface passes a context the local document store does not take
func (e *docEngine) VersionWeight(_ context.Context) (plug.VersionWeightValue, error) {
	return pluggedWeight(e.ds.VersionWeight())
}

// VersionWeight is what the table's versions weigh, in one statement on the
// transaction ctx carries for the database, else on the pool, within the
// database's timeout.
func (e *sqlEngine) VersionWeight(ctx context.Context) (plug.VersionWeightValue, error) {
	c, done, err := e.call(ctx, false)
	if err != nil {
		return plug.VersionWeightValue{}, err
	}
	defer done()
	return pluggedWeight(e.ds.VersionWeight(c))
}

// VersionWeight is what the versions of the engine a history wraps weigh.
func (h *historied) VersionWeight(ctx context.Context) (plug.VersionWeightValue, error) {
	return engineVersionWeight(ctx, h.StoreEngine)
}

// VersionWeight is what a sealing store's versions weigh as they rest.
func (e *sealedEngine) VersionWeight(ctx context.Context) (plug.VersionWeightValue, error) {
	return engineVersionWeight(ctx, e.inner)
}

// engineVersionWeight is what eng's versions weigh, or VersionsNotKept for
// an engine that keeps none.
func engineVersionWeight(ctx context.Context, eng plug.StoreEngine) (plug.VersionWeightValue, error) {
	if vw, ok := eng.(plug.VersionWeighter); ok {
		return vw.VersionWeight(ctx)
	}
	return plug.VersionWeightValue{}, docstore.VersionsNotKept
}

// weighVersions sets on info what the store's versions weigh, on a running
// product, when the store keeps revisions and its engine says: nothing when
// it cannot — the Studio then shows how many versions a store keeps, as
// before.
func (s *storeCore) weighVersions(a *App, info *model.StoreHistory) {
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
