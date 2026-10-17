package app

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The kinds of privacy option.
const (
	optionErase     = "erase"
	optionDelete    = "delete"
	optionHeld      = "held-until"
	optionAnonymise = "anonymise"
	optionPurpose   = "purpose"
	optionErasure   = "delete-on-erasure"
	optionByProduct = "by-product"
)

// Retention (ADR 0006 §5). A store says how long its records keep their
// personal data, and when they go: the count starts from an instant the
// entity carries, as a workflow's At does.
//
//	var Reports = Service.Store("reports", Report.Key,
//		app.EraseAfter(90*24*time.Hour, Report.Closed),
//		app.DeleteAfter(deleteAfter, Report.Closed),
//		app.Purpose("Handle the notices users file and answer them"))
//
// To erase a record, kit clears its personal, special and secret members and
// its subject, after the store's Anonymise function has kept what it
// generalises; it stamps the erased fields. To delete one, it removes it. A
// held record is neither erased nor deleted. Each store with a retention has
// a loop of the daemon, "<store> retention", which sleeps until the next
// record is due — an agenda kept from the store's writes, never a poll —
// erases and deletes what is due, and folds the store so that what it
// overwrote leaves its files. KIT_RETENTION sets what it does: on, dry-run
// (it journals what it would do and changes nothing) or off. A store whose
// product keeps its retention itself says so (RetentionByProduct): kit runs
// none there, and asks for none.

// privacyOption is one privacy option of a store: a retention — kit's, or
// the product's own —, a hold until an instant, an anonymiser, a purpose.
// Its function is typed for the store's entity; the store checks the type
// when it is declared.
type privacyOption struct {
	kind string
	// delay is a fixed delay; setting one read from a setting.
	delay   time.Duration
	setting *SettingService[time.Duration]
	delayed bool
	// fn is func(T) (time.Time, bool) for a retention or a hold, func(*T)
	// for an anonymiser.
	fn any
	// text is a purpose, a hold's legal ground, or the product's own time
	// limits.
	text string
}

// storePrivacy is what a store declares about personal data, checked for its
// entity type — each function wrapped once to take the entity as an any —,
// and its retention while the app runs.
type storePrivacy struct {
	erase, delete *retentionRule
	heldUntil     func(v any) (time.Time, bool)
	heldAt        *pos
	heldReason    string
	// anonymise is the store's Anonymise function on a copy of the entity,
	// which it returns changed.
	anonymise     func(v any) any
	anonymiseAt   *pos
	purpose       string
	erasureDelete bool
	// byProduct is the retention the product keeps itself, in its own
	// words (RetentionByProduct); nil when kit keeps it.
	byProduct *model.ProductRetention

	mu  sync.Mutex
	run *retentionRun
	// erased and deleted count what this run erased and deleted, for the
	// graph; rekeyed says a key built from personal data was met.
	erased, deleted int
	rekeyed         bool
}

// retentionRule is when a store erases or deletes a record.
type retentionRule struct {
	delay   time.Duration
	setting *SettingService[time.Duration]
	// since gives the instant the delay counts from; at, without a delay,
	// the instant itself.
	since, at func(v any) (time.Time, bool)
	fnAt      *pos
}

// privacyDecl checks a store's privacy options, one by one: each function
// must read the store's own entity type, each rule be given once, each delay
// be positive. A mistake is a declaration problem at the store's position.
type privacyDecl struct {
	s   *storeCore
	svc *Service
	p   *storePrivacy
	// fns reads an option's function as one of the store's entity type.
	fns privacyFuncs
}

// privacyFuncs are an option's function read for one entity type, and
// wrapped to take the entity as an any: an instant of a record — a
// retention's, a hold's —, or an anonymiser. ok is false for a function of
// another type.
type privacyFuncs struct {
	instant   func(fn any) (wrapped func(v any) (time.Time, bool), ok, isNil bool)
	anonymise func(fn any) (wrapped func(v any) any, ok, isNil bool)
}

// privacyFuncsOf are the option functions of an entity of type T.
func privacyFuncsOf[T any]() privacyFuncs {
	return privacyFuncs{
		instant: func(fn any) (wrapped func(v any) (time.Time, bool), ok, isNil bool) {
			typed, ok := fn.(func(T) (time.Time, bool))
			if !ok || typed == nil {
				return nil, ok, typed == nil
			}
			return func(v any) (time.Time, bool) { return typed(as[T](v)) }, true, false
		},
		anonymise: func(fn any) (wrapped func(v any) any, ok, isNil bool) {
			typed, ok := fn.(func(*T))
			if !ok || typed == nil {
				return nil, ok, typed == nil
			}
			return func(v any) any {
				t := as[T](v)
				typed(&t)
				return t
			}, true, false
		},
	}
}

// storeConfigure sets the option on what it configures.
func (o *privacyOption) storeConfigure(so *storeOptions) { so.privacy = append(so.privacy, o) }

// withDelay sets a retention's delay: a duration, or a duration setting.
func (o *privacyOption) withDelay[D Delay](d D) *privacyOption {
	o.delayed = true
	switch v := any(d).(type) {
	case time.Duration:
		o.delay = v
	case *SettingService[time.Duration]:
		o.setting = v
	}
	return o
}

// EraseAfter erases a record's personal data delay after the instant since
// returns — its closing, say; false means "not yet": an open record is never
// due. delay is a duration, or a duration setting.
func EraseAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreConfigurer {
	return (&privacyOption{kind: optionErase, fn: since}).withDelay(delay)
}

// EraseAt erases a record's personal data at the instant at returns: one the
// product computed and stored on the record — a retention in calendar
// months, one that depends on a tenant.
func EraseAt[T any](at func(T) (time.Time, bool)) StoreConfigurer {
	return &privacyOption{kind: optionErase, fn: at}
}

// DeleteAfter deletes a record delay after the instant since returns.
func DeleteAfter[T any, D Delay](delay D, since func(T) (time.Time, bool)) StoreConfigurer {
	return (&privacyOption{kind: optionDelete, fn: since}).withDelay(delay)
}

// DeleteAt deletes a record at the instant at returns.
func DeleteAt[T any](at func(T) (time.Time, bool)) StoreConfigurer {
	return &privacyOption{kind: optionDelete, fn: at}
}

// Anonymise says what an erased record keeps: fn runs first, and generalises
// into unclassified fields — a birth date to its year, an address to its
// region; then kit clears the rest. kit cannot prove that what remains
// identifies no one: by declaring the function, the product asserts it.
func Anonymise[T any](fn func(*T)) StoreConfigurer {
	return &privacyOption{kind: optionAnonymise, fn: fn}
}

// HeldUntil holds each record until the instant until returns: the law's own
// retention, such as an invoice kept ten years (GDPR art. 17(3)(b)). reason
// is the legal ground, for the register. A held record is exported and
// updated like any other, and neither erased nor deleted.
func HeldUntil[T any](until func(T) (time.Time, bool), reason string) StoreConfigurer {
	return &privacyOption{kind: optionHeld, fn: until, text: reason}
}

// Purpose says why the store keeps its records (GDPR art. 30(1)(b)): the
// register lists it, and a person's export carries it.
func Purpose(text string) StoreConfigurer {
	return &privacyOption{kind: optionPurpose, text: text}
}

// DeleteOnErasure makes a person's erasure (app.Erase) delete the store's
// records instead of clearing them, as sessions and accounts want.
func DeleteOnErasure() StoreConfigurer {
	return &privacyOption{kind: optionErasure}
}

// RetentionByProduct says the product keeps the store's retention itself:
// its own code erases and deletes what it no longer needs — a record whose
// life the product's own rules end, a lifecycle kit cannot read from the
// entity. limits says, in the product's words, for how long and how, which
// the register publishes as the store's time limits (GDPR art. 30(1)(f));
// "" says only that the product keeps them.
//
// kit then runs no retention for the store, and warns of neither a missing
// retention nor a missing subject. Its classified fields keep every other
// promise: sealed at rest, never shown, exported with their person — when
// the store names one — and erased by app.Erase and Store.Erase. It goes
// with app.Purpose, app.HeldUntil, app.Anonymise and app.DeleteOnErasure;
// with app.EraseAfter, app.EraseAt, app.DeleteAfter or app.DeleteAt it is
// refused: a store's retention is kit's or the product's, never both.
func RetentionByProduct(limits string) StoreConfigurer {
	return &privacyOption{kind: optionByProduct, text: limits}
}

// wait is the rule's delay: its setting's value in the app that runs it, or
// the duration it was declared with.
func (r *retentionRule) wait() time.Duration {
	if r.setting != nil {
		return r.setting.Get()
	}
	return r.delay
}

// due is when the rule makes v due, and whether it does. A function that
// panics makes nothing due: the loop says so.
func (r *retentionRule) due(v any) (at time.Time, ok bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			at, ok, err = time.Time{}, false, failure(CodeRetentionPanic, "RETENTION_PANICKED", "a retention function panicked", nil, errs.String("panic", fmt.Sprint(p)))
		}
	}()
	if r.at != nil {
		at, ok = r.at(v)
		return at, ok, nil
	}
	since, ok := r.since(v)
	if !ok {
		return time.Time{}, false, nil
	}
	return since.Add(r.wait()), true, nil
}

// declarePrivacy checks a store's privacy options once it is registered,
// and keeps them.
func (s *StoreService[T]) declarePrivacy(svc *Service, opts []*privacyOption) {
	s.declarePrivacyOf(svc, opts, privacyFuncsOf[T]())
}

// declarePrivacyOf checks a store's privacy options, their functions read as
// fns reads them, and keeps them.
func (s *storeCore) declarePrivacyOf(svc *Service, opts []*privacyOption, fns privacyFuncs) {
	if len(opts) == 0 {
		return
	}
	d := privacyDecl{s: s, svc: svc, p: &storePrivacy{}, fns: fns}
	for _, o := range opts {
		d.take(o)
	}
	if d.p.byProduct != nil && d.p.hasRetention() {
		svc.problem(s.decl, s.id, "privacy.by-product-retention", "store", s.name)
	}
	s.privacy = d.p
}

// take checks one option and keeps it.
func (d *privacyDecl) take(o *privacyOption) {
	if d.takeWords(o) {
		return
	}
	switch o.kind {
	case optionAnonymise:
		d.anonymiser(o)
	case optionHeld:
		d.held(o)
	case optionErase:
		d.p.erase = d.rule(o, d.p.erase)
	case optionDelete:
		d.p.delete = d.rule(o, d.p.delete)
	default:
		// Every other kind has nothing to check or name here.
	}
}

// takeWords keeps an option that says something of the store rather than
// a function of its records — its purpose, its deletion on erasure, the
// retention its product keeps — and reports whether o was one.
func (d *privacyDecl) takeWords(o *privacyOption) bool {
	switch o.kind {
	case optionPurpose:
		d.p.purpose = strings.TrimSpace(o.text)
	case optionErasure:
		d.p.erasureDelete = true
	case optionByProduct:
		if !d.refused(o, true, false, d.p.byProduct != nil) {
			d.p.byProduct = &model.ProductRetention{Limits: strings.TrimSpace(o.text)}
		}
	default:
		return false
	}
	return true
}

// anonymiser keeps the store's Anonymise function.
func (d *privacyDecl) anonymiser(o *privacyOption) {
	fn, ok, isNil := d.fns.anonymise(o.fn)
	if d.refused(o, ok, isNil, d.p.anonymise != nil) {
		return
	}
	d.p.anonymise = fn
	if at, _ := funcInfo(o.fn); at.file() != "" {
		d.p.anonymiseAt = &at
	}
}

// held keeps the store's HeldUntil and its legal ground.
func (d *privacyDecl) held(o *privacyOption) {
	fn, ok, isNil := d.fns.instant(o.fn)
	if d.refused(o, ok, isNil, d.p.heldUntil != nil) {
		return
	}
	d.p.heldUntil, d.p.heldReason = fn, strings.TrimSpace(o.text)
	if at, _ := funcInfo(o.fn); at.file() != "" {
		d.p.heldAt = &at
	}
}

// rule checks an erasure's or a deletion's option and returns the store's
// rule: the one it builds, or prev when the option is refused.
func (d *privacyDecl) rule(o *privacyOption, prev *retentionRule) *retentionRule {
	fn, ok, isNil := d.fns.instant(o.fn)
	if d.refused(o, ok, isNil, prev != nil) {
		return prev
	}
	r := &retentionRule{}
	if at, _ := funcInfo(o.fn); at.file() != "" {
		r.fnAt = &at
	}
	if !o.delayed {
		r.at = fn
		return r
	}
	r.since = fn
	return d.delayed(o, r, prev)
}

// delayed gives a rule its delay — a setting's, which then says the rule
// waits on it, or a positive duration — and returns it; prev when the delay
// is refused.
func (d *privacyDecl) delayed(o *privacyOption, r, prev *retentionRule) *retentionRule {
	s := d.s
	switch {
	case o.setting != nil:
		r.setting = o.setting
		o.setting.waitedBy = append(o.setting.waitedBy, say("retention.waiter", "what", o.kind, "store", s.id))
	case o.delay <= 0:
		d.svc.problem(s.decl, s.id, "privacy.delay", "store", s.name, "option", optionName(o))
		return prev
	default:
		r.delay = o.delay
	}
	return r
}

// refused says what is wrong with an option's function, when something is:
// it reads another type than the store's entity, it is nil, or the option
// was given before.
func (d *privacyDecl) refused(o *privacyOption, typed, isNil, twice bool) bool {
	s := d.s
	switch {
	case !typed:
		d.svc.problem(s.decl, s.id, "privacy.option-type", "store", s.name, "type", s.ent.typ, "option", optionName(o), "reads", keyType(o.fn))
	case isNil:
		d.svc.problem(s.decl, s.id, "privacy.option-nil", "store", s.name, "option", optionName(o))
	case twice:
		d.svc.problem(s.decl, s.id, "privacy.option-twice", "store", s.name, "option", twiceName(o))
	default:
		return false
	}
	return true
}

// optionName is how a declaration problem names an option.
func optionName(o *privacyOption) string {
	switch o.kind {
	case optionErase:
		if o.delayed {
			return "app.EraseAfter"
		}
		return "app.EraseAt"
	case optionDelete:
		if o.delayed {
			return "app.DeleteAfter"
		}
		return "app.DeleteAt"
	case optionHeld:
		return "app.HeldUntil"
	case optionAnonymise:
		return "app.Anonymise"
	case optionByProduct:
		return "app.RetentionByProduct"
	default:
		// Every other kind has nothing to check or name here.
	}
	return o.kind
}

// twiceName is how an option given twice is named: the rule an erasure's
// or a deletion's option gives, whichever function gives it, or the option.
func twiceName(o *privacyOption) string {
	if o.kind == optionErase || o.kind == optionDelete {
		return o.kind
	}
	return optionName(o)
}

// hasRetention reports whether the store erases or deletes on its own.
func (p *storePrivacy) hasRetention() bool {
	return p != nil && (p.erase != nil || p.delete != nil)
}

// resolveRetention reads KIT_RETENTION. A value it does not know is a
// problem the start reports, and the retention stays off meanwhile.
func resolveRetention(getenv func(string) string) (mode string, problem bool) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(model.VarRetention))); v {
	case "", model.RetentionOn:
		return model.RetentionOn, false
	case model.RetentionDryRun, model.RetentionOff:
		return v, false
	}
	return model.RetentionOff, true
}

// retentionMode is the app's KIT_RETENTION, read at its start.
func (a *App) retentionMode() string {
	mode, _ := resolveRetention(os.Getenv)
	return mode
}

// retentionSetting is KIT_RETENTION as the configuration shows it.
func (a *App) retentionSetting() model.Setting {
	s := model.Setting{Name: model.VarRetention, Value: a.retentionMode(), From: model.SettingDefault}
	if os.Getenv(model.VarRetention) != "" {
		s.From = model.SettingEnv
	}
	return s
}
