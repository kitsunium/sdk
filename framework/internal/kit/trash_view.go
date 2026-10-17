package kit

import (
	"context"
	"net/http"
	"strconv"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// What the Studio sees of a store's trash (ADR 0178 C4, amended on
// 2026-10-15): each record the product deleted, by its key, with when it was
// deleted and when kit purges it — never its value. A key that reads a
// personal, special or secret member is hidden, as the data browser hides
// that member; a page counts records by offset, so the cursor never quotes a
// key. The route sits behind the Studio's guard (devtools.go); restoring a
// record is the product's (Store.Untrash): the Studio reads (D13).

// maxTrashed bounds a page of the trash list.
const maxTrashed int = 1000

// defaultTrashed is how many records a page lists when the Studio does not
// say.
const defaultTrashed int = 200

// trashSource is a store the Studio lists the trash of, whatever its entity
// type.
type trashSource interface {
	trashShown(ctx context.Context, offset, limit int) (model.StoreTrash, error)
}

// trashOf is the store id names, as a trash the Studio lists: a NotFound
// when the app has no such store, an Invalid when the store keeps no trash
// — both CodeTrashList.
//
// IFACE-OPAQUE: a store's entity type is its own, so the Studio reaches
// every Store[T] through the one interface its route needs, as it reaches
// a record's versions (revisionsSource).
func (a *App) trashOf(id string) (trashSource, error) {
	n := a.findNode(id)
	src, ok := n.(trashSource)
	if !ok {
		public := "no such store: " + clip(id)
		return nil, NotFound(public).Wrap(failure(CodeTrashList, "TRASH_STORE_UNKNOWN", public, nil, errs.String("store", clip(id))))
	}
	if keeps, ok := n.(trashRunner); ok && !keeps.keepsTrash() {
		public := clip(id) + " keeps no trash: declare it with kit.Revisions(n)"
		return nil, Invalid(public).Wrap(failure(CodeTrashList, "TRASH_NOT_KEPT", public, nil, errs.String("store", clip(id))))
	}
	return src, nil
}

// trashShown is the page of the store's trash from offset, at most limit
// records, in key order: each key — or Hidden —, deletion and purge instant.
// It reads the store as the data browser does, in no span: the Studio is no
// caller of the product's.
func (s *storeCore) trashShown(ctx context.Context, offset, limit int) (model.StoreTrash, error) {
	out := model.StoreTrash{Store: s.id, Records: []model.TrashedRecord{}}
	a := s.app()
	tr, err := s.trashKeeper()
	if err != nil {
		return out, err
	}
	stones, err := tr.Tombstones(s.outside(ctx))
	if err != nil {
		return out, s.trashFailure(s.trashSaid(err, ""))
	}
	out.Total = len(stones)
	if offset >= len(stones) {
		return out, nil
	}
	end := min(offset+limit, len(stones))
	for _, st := range stones[offset:end] {
		rec := model.TrashedRecord{At: st.At, PurgeAt: s.purgeAt(ctx, a, st)}
		if s.keyShown(ctx, st.Key) {
			rec.Key = st.Key
		} else {
			rec.Hidden = true
		}
		out.Records = append(out.Records, rec)
	}
	if end < len(stones) {
		out.Next = end
	}
	return out, nil
}

// keyShown reports whether the Studio may see the key of the record trashed
// under key: the store's type holds no personal, special or secret member,
// or the record, those members cleared, still gives the key. A record that
// no longer opens says nothing, so its key is hidden.
func (s *storeCore) keyShown(ctx context.Context, key string) bool {
	p := s.plan()
	if !p.sensitive() {
		return true
	}
	v, err := s.trashedValue(ctx, key)
	if err != nil {
		return false
	}
	rv := s.addressable(v)
	p.clearSensitive(rv)
	return s.keyOf(rv.Interface()) == key
}

// serveTrash answers GET /_kit/api/trash?store=[&offset=&limit=]: a page of
// a store's trash, as the Studio's data view lists it.
func (a *App) serveTrash(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	src, err := a.trashOf(q.Get("store"))
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	offset, limit, err := trashPage(q.Get("offset"), q.Get("limit"))
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	out, err := src.trashShown(r.Context(), offset, limit)
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// trashPage reads a page of the trash list: an offset from 0, 0 unless
// given; a limit from 1 to maxTrashed, defaultTrashed unless given.
func trashPage(offsetText, limitText string) (offset, limit int, err error) {
	limit = defaultTrashed
	if offsetText != "" {
		n, perr := strconv.Atoi(offsetText)
		if perr != nil || n < 0 {
			return 0, 0, Invalid("offset must be a number from 0")
		}
		offset = n
	}
	if limitText != "" {
		n, perr := strconv.Atoi(limitText)
		if perr != nil || n < 1 || n > maxTrashed {
			return 0, 0, Invalid("limit must be a number from 1 to " + strconv.Itoa(maxTrashed))
		}
		limit = n
	}
	return offset, limit, nil
}
