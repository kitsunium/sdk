package kit

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/security/redact"
)

// What the model and the Studio see of what a store remembers (ADR 0007 §5,
// §6): the fields that keep their former values, the password policies,
// what the histories weigh on a running product, and a record's former
// values — when each was replaced and by whom, a value shown only where the
// data browser would show the field.

// historyInfo describes what the store remembers, for the model: nil when
// it remembers nothing, keeps no revisions and has no password policy.
func (s *StoreService[T]) historyInfo(a *App) *model.StoreHistory {
	keeps := s.keeps()
	if len(keeps) == 0 && len(s.passwords) == 0 && s.revisions == 0 {
		return nil
	}
	info := &model.StoreHistory{Revisions: s.revisions, Fields: slices.Sorted(maps.Keys(keeps))}
	for _, p := range s.passwords {
		info.Passwords = append(info.Passwords, p.info(a))
	}
	if h := s.historied(); h != nil && a != nil && a.running() {
		kept, size := h.weigh(context.Background())
		info.Kept, info.Bytes = &kept, &size
	}
	return info
}

// weigh is how many former values the store keeps, and what its histories
// weigh as kept.
func (h *historied[T]) weigh(ctx context.Context) (kept int, size int64) {
	if h.hist == nil {
		return 0, 0
	}
	entries, err := h.hist.Entries(ctx, 0)
	if err != nil {
		return 0, 0
	}
	for _, raw := range entries {
		size += int64(len(raw))
		var doc historyRecord
		if json.Unmarshal(raw, &doc) == nil {
			for _, list := range doc.Fields {
				kept += len(list)
			}
		}
	}
	return kept, size
}

// placeholder is a redacted value, as JSON; sealedShown a value sealed at
// rest, as the Studio shows it.
var (
	placeholder = json.RawMessage(strconv.Quote(redact.Placeholder))
	sealedShown = json.RawMessage(strconv.Quote(model.SealedPlaceholder))
)

// shownFormer is a former value of the field at pointer as the Studio may
// show it: none for a secret's, the placeholder for a personal or special
// one's — the subject's included — or a field named like a secret, and any
// other read as its field's type, so that its own classified members, and
// the names the redactor knows, are redacted as a record's are.
func (p *classPlan) shownFormer(pointer string, value json.RawMessage) json.RawMessage {
	switch p.classOf(pointer) {
	case model.ClassSecret:
		return nil
	case model.ClassPersonal, model.ClassSpecial:
		return placeholder
	}
	m := p.member(pointer)
	if m == nil || names.Name(lastSegment(pointer)) {
		return placeholder
	}
	v := reflect.New(m.typ)
	if json.Unmarshal(value, v.Interface()) != nil {
		return placeholder
	}
	shown, _ := redactValue(v.Elem().Interface(), 64<<10)
	return shown
}

// lastSegment is the member a JSON pointer ends at, unescaped (RFC 6901).
func lastSegment(pointer string) string {
	last := pointer[strings.LastIndex(pointer, "/")+1:]
	return strings.ReplaceAll(strings.ReplaceAll(last, "~1", "/"), "~0", "~")
}

// formerSource is a store the Studio reads a record's former values from,
// whatever its entity type.
type formerSource interface {
	formerShown(ctx context.Context, key string) (model.RecordHistory, error)
}

// formerShown is what the Studio shows of the record under key's former
// values: when each was replaced and by whom, every field's — a secret's
// too, without its value —, each value as shownFormer says. It reads the
// store as the data browser does, in no span: the Studio is no caller of
// the product's.
func (s *StoreService[T]) formerShown(ctx context.Context, key string) (model.RecordHistory, error) {
	h := s.historied()
	if h == nil || len(h.keeps) == 0 {
		return model.RecordHistory{}, Invalid(s.name + " keeps no former values: tag a field history=N")
	}
	all, err := h.formerAll(ctx, key)
	if err != nil {
		return model.RecordHistory{}, err
	}
	plan, out := s.plan(), model.RecordHistory{Fields: map[string][]model.Former{}}
	for p, list := range all {
		for _, e := range list {
			shown := plan.shownFormer(p, e.Value)
			if e.sealed && shown != nil {
				// A former value sealed at rest is shown sealed (ADR 0006 §9).
				shown = sealedShown
			}
			out.Fields[p] = append(out.Fields[p], Former{Value: shown, Until: e.Until, By: e.By})
		}
	}
	return out, nil
}

// serveFormer answers GET /_kit/api/former?store=&key=: a record's former
// values, as the Studio's data view shows them.
func (a *App) serveFormer(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	st, ok := a.findNode(q.Get("store")).(formerSource)
	if !ok {
		a.replyError(r.Context(), w, NotFound("no such store"))
		return
	}
	out, err := st.formerShown(r.Context(), q.Get("key"))
	if err != nil {
		a.replyError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
