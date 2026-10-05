package kit

import (
	"os"

	"github.com/kitsunium/sdk/framework/model"
)

// What the model and the start say of a store's personal data (ADR 0006
// §1, §12).

// privacyInfo describes the store's personal data for the model: its
// subject, purpose and retention, and on a runtime graph what is held and
// due and what this run erased and deleted.
func (s *StoreService[T]) privacyInfo(a *App) *model.StorePrivacy {
	plan, p := s.plan(), s.privacy
	if !plan.personal() && p == nil {
		return nil
	}
	info := &model.StorePrivacy{Subject: plan.subjectPointer()}
	if p != nil {
		p.describe(a, info)
	}
	if a != nil && a.running() && s.engine() != nil {
		s.describeRun(a, info)
	}
	return info
}

// describe fills info with what the store declares.
func (p *storePrivacy[T]) describe(a *App, info *model.StorePrivacy) {
	info.Purpose, info.DeleteOnErasure = p.purpose, p.erasureDelete
	info.Erase, info.Delete = retentionInfo(a, p.erase), retentionInfo(a, p.delete)
	if p.byProduct != nil {
		info.ByProduct = new(*p.byProduct)
	}
	if p.heldUntil != nil {
		info.HeldUntil = &model.HeldUntil{At: a.source(p.heldAt), Reason: p.heldReason}
	}
	info.Anonymise = a.source(p.anonymiseAt)
}

// describeRun fills info with what the running store holds and did: how
// many of its records are held, when the next is due, what this run erased
// and deleted.
func (s *StoreService[T]) describeRun(a *App, info *model.StorePrivacy) {
	info.Held = new(s.heldCount(a))
	if r := s.retention(); r != nil {
		if first := r.earliest(); !first.IsZero() {
			info.Due = new(first.UTC())
		}
	}
	if p := s.privacy; p != nil {
		p.mu.Lock()
		erased, deleted := p.erased, p.deleted
		p.mu.Unlock()
		info.Erased, info.Deleted = &erased, &deleted
	}
}

// privacyProblems judges what the app does with personal data: the kit tags
// of every type it shows or keeps (errors), KIT_RETENTION, and — when kit
// explains — a store that keeps personal data with no retention or no
// subject, unless the product keeps its retention, and fields whose names
// read like personal data (warnings).
func (a *App) privacyProblems() []model.Diagnostic {
	a.privacy.mu.Lock()
	explain := a.privacy.explain
	a.privacy.mu.Unlock()
	out := append(a.classificationProblems(explain), a.retentionProblems()...)
	if explain {
		out = append(out, a.storeWarnings()...)
	}
	return out
}

// retentionProblems judges KIT_RETENTION: a value kit does not know refuses
// the start; off is warned of outside dev.
func (a *App) retentionProblems() []model.Diagnostic {
	if _, bad := resolveRetention(os.Getenv); bad {
		return []model.Diagnostic{diagnosticOf("error", "", nil, say("retention.invalid", "variable", model.VarRetention))}
	}
	if a.retentionMode() == model.RetentionOff && a.cfg.env != EnvDev && a.keepsPersonalData() {
		return []model.Diagnostic{diagnosticOf("warning", "", nil, say("retention.off", "variable", model.VarRetention))}
	}
	return nil
}

// storeWarnings are the warnings of the stores that keep personal data with
// no retention, or with no subject — none for a store whose product keeps
// its retention itself (RetentionByProduct), which asks kit for neither.
func (a *App) storeWarnings() []model.Diagnostic {
	var out []model.Diagnostic
	for _, st := range a.productStores() {
		if st.plan().personal() {
			out = append(out, a.storeWarningsOf(st)...)
		}
	}
	return out
}

// storeWarningsOf are the warnings of st, a store that keeps personal data.
func (a *App) storeWarningsOf(st privacyStore) []model.Diagnostic {
	info := st.privacyInfo(a)
	if info != nil && info.ByProduct != nil {
		return nil
	}
	var out []model.Diagnostic
	b := st.base()
	if info == nil || (info.Erase == nil && info.Delete == nil) {
		out = append(out, diagnosticOf("warning", b.id, a.source(&b.decl), say("privacy.no-retention", "store", b.id)))
	}
	if st.plan().subject == nil {
		out = append(out, diagnosticOf("warning", b.id, a.source(&b.decl), say("privacy.no-subject", "store", b.id)))
	}
	return out
}
