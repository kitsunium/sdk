package kit

import (
	"context"
	"net/http"
	"slices"
	"strconv"

	"github.com/kitsunium/sdk/framework/model"
)

// What the Studio sees of a record's versions (ADR 0007 §5, §6): each as the
// data browser shows a record — a member sealed at rest sealed, a personal,
// special or secret one redacted —, what changed between any two, a value
// given only where the data browser would show it — a personal, special or
// secret member's edit says it changed. The route sits behind the Studio's
// guard (devtools.go); restoring a version is the command line's
// (revisionscmd.go): the Studio reads (ADR 0010, D13).

// revisionsSource is a store the Studio reads a record's versions from, and
// the command line restores one of, whatever its entity type.
type revisionsSource interface {
	revisionsShown(ctx context.Context, key string, from, to uint64) (model.RecordVersions, error)
	restoreVia(ctx context.Context, a *App, via, key string, number uint64, fields []string) error
}

// revisionsShown is what the Studio shows of the versions of the record
// under key, and of what changed from version from to version to when both
// are given. It reads the store as the data browser does, in no span: the
// Studio is no caller of the product's.
func (s *StoreService[T]) revisionsShown(ctx context.Context, key string, from, to uint64) (model.RecordVersions, error) {
	resting, err := s.restingVersions(ctx, key)
	if err != nil {
		return model.RecordVersions{}, err
	}
	out := model.RecordVersions{Versions: make([]model.RecordVersion, 0, len(resting))}
	for _, v := range resting {
		out.Versions = append(out.Versions, model.RecordVersion{
			Number: v.Number, At: v.At, By: v.Meta[metaBy],
			Command: v.Meta[metaCommand], Value: s.redacted(v.JSON),
		})
	}
	if from == 0 && to == 0 {
		return out, nil
	}
	edits, err := s.diff(ctx, key, from, to)
	if err != nil {
		return model.RecordVersions{}, err
	}
	out.From, out.To, out.Edits = from, to, s.plan().shownEdits(edits)
	return out, nil
}

// shownEdits are edits as the Studio may show them: an edit at a personal,
// special or secret member, the subject, or a member named like a secret —
// or whose value holds one — says it changed and carries no value; any
// other value is redacted as a payload is, by the names the redactor knows.
func (p *classPlan) shownEdits(edits []Edit) []Edit {
	out := make([]Edit, 0, len(edits))
	for _, e := range edits {
		if p.hiddenAt(e.Path) {
			out = append(out, Edit{Op: e.Op, Path: e.Path})
			continue
		}
		shown := Edit{Op: e.Op, Path: e.Path}
		if len(e.From) > 0 {
			shown.From, _ = redactJSON(e.From, 64<<10)
		}
		if len(e.To) > 0 {
			shown.To, _ = redactJSON(e.To, 64<<10)
		}
		out = append(out, shown)
	}
	return out
}

// hiddenAt reports whether the Studio shows no value at path, a concrete
// JSON pointer: it lies in a member kit redacts, holds one, or names a
// member the redactor knows as a secret's.
func (p *classPlan) hiddenAt(path string) bool {
	segs := splitPointer(path)
	if slices.ContainsFunc(segs, names.Name) {
		return true
	}
	if len(segs) == 0 {
		return p.sensitive()
	}
	for _, m := range p.members {
		if !m.tag.sensitive() {
			continue
		}
		pattern := splitPointer(m.pointer)
		n := min(len(pattern), len(segs))
		if matchSegs(pattern[:n], segs[:n]) {
			return true // the path lies in the member, or holds it
		}
	}
	return false
}

// matchSegs reports whether a member's pointer segments — "*" for every
// element — match a concrete path's, one for one.
func matchSegs(pattern, segs []string) bool {
	for i := range pattern {
		if pattern[i] != "*" && pattern[i] != segs[i] {
			return false
		}
	}
	return true
}

// serveRevisions answers GET /_kit/api/revisions?store=&key=[&from=&to=]: a
// record's versions, and what changed between two of them, as the Studio's
// data view shows them.
func (a *App) serveRevisions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, ok := a.findNode(q.Get("store")).(revisionsSource)
	if !ok {
		a.replyError(r.Context(), w, NotFound("no such store"))
		return
	}
	from, to, err := versionPair(q.Get("from"), q.Get("to"))
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	out, err := st.revisionsShown(r.Context(), q.Get("key"), from, to)
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// versionPair reads the two versions a diff compares: both, or neither.
func versionPair(fromText, toText string) (from, to uint64, err error) {
	if fromText == "" && toText == "" {
		return 0, 0, nil
	}
	from, ferr := strconv.ParseUint(fromText, 10, 64)
	to, terr := strconv.ParseUint(toText, 10, 64)
	if ferr != nil || terr != nil || from == 0 || to == 0 {
		return 0, 0, Invalid("from and to are two version numbers, from 1")
	}
	return from, to, nil
}
