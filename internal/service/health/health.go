// Package health implements the SDK's process-health domain: the registry
// behind core/health.Health, the per-check budget, the bounded result cache,
// the three HTTP handlers, and the opt-in lifecycle and sd_notify wiring.
// Admitted by ADR 0060.
//
// # What the handler hides
//
// A probe endpoint is exposed more widely than whoever added it expected. The
// body therefore never carries a check's raw error: a handler renders the
// deepest errs Public it can find, and a fixed SDK string when there is none.
// A caller's own typed error keeps its identity through origin-wins, so a
// Public they wrote — already validated wire-safe by errs — is what a stranger
// reads, while `dial tcp 10.0.3.14:5432: connect: connection refused` is
// replaced wholesale rather than trimmed. The full error goes to
// Config.OnReport, which is the caller's own log.
//
// # What a budget expiring means
//
// Exactly three things: the run's context is cancelled (an announcement), the
// registry stops waiting, and a [CheckTimeout] result is recorded. No
// goroutine is killed — Go cannot — and nothing the check holds is closed on
// its behalf. A check that outlives its budget keeps ONE goroutine until it
// returns, and the next probe joins that same run instead of starting another;
// see [inflight].
package health

import (
	"slices"
	"sync"

	corehealth "github.com/kitsunium/sdk/internal/core/health"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// health is the concrete core/health.Health. It is unexported: there is one
// canonical way to answer the three probes, so a registry of registries would
// be over-abstraction (the proc, resilience, scheduler and lifecycle
// precedents — ADR 0016 / ADR 0026 / ADR 0041 / ADR 0050).
type health struct {
	// cfg is the resolved configuration; budgets are clamped through it.
	cfg Config
	// clk is the injected time source; never package time.
	clk clock.Timed
	// hookMu serialises OnReport so the hook need not be concurrency-safe.
	hookMu sync.Mutex
	// mu guards the registration sets and the three state fields below.
	//
	// It is an RWMutex because the two hot paths — [health.phase], read once
	// per probe, and [health.entriesFor], read once per probe per probe kind —
	// take nothing away from it, while every writer runs at registration or
	// once per transition.
	mu sync.RWMutex
	// checks holds one slice per probe, each in registration order — which is
	// the order results are reported in, so two identical probes render
	// identically and a diff of two responses is readable.
	checks map[corehealth.Probe][]*entry
	// names is the duplicate index, keyed per probe: the same name may appear
	// once on readiness and once on liveness, because those are two answers
	// about different evidence rather than a collision.
	names map[corehealth.Probe]map[string]bool
	// startupPending counts registered startup checks that have yet to pass.
	// Zero — including the zero of a registry with no startup checks at all —
	// means the process is past its startup phase.
	startupPending int
	// draining records that Drain was called. It is one-way; see Drain.
	draining bool
	// notified records that READY=1 has been sent, so it is sent once.
	notified bool
	// lastNotified is the aggregate readiness status last announced, so a
	// STATUS= datagram is sent on change rather than on every poll.
	lastNotified corehealth.Status
}

// New returns a Health built from cfg. It cannot fail: a nil Clock falls back
// to clock.System, a non-positive DefaultTimeout to [DefaultCheckTimeout], and
// nil hooks to no observation — all working configurations rather than inert
// ones (ADR 0031). What CAN fail — an unrunnable check, a duplicate name, a
// cache window the SDK will not vouch for — fails at registration, where the
// caller made the mistake.
//
// A registry with no checks is legitimate and answers every probe healthy: the
// process replying IS the evidence that it is running.
func New(cfg Config) corehealth.Health {
	//: both maps are built eagerly so no Add has to check for a nil map.
	return &health{
		cfg:    cfg,
		clk:    cfg.resolvedClock(),
		checks: map[corehealth.Probe][]*entry{},
		names:  map[corehealth.Probe]map[string]bool{},
		//: the aggregate nobody has announced yet is the conservative one.
		lastNotified: corehealth.StatusUnhealthy,
	}
}

// AddStartup registers a check that gates the startup probe.
func (h *health) AddStartup(check corehealth.StartupCheckValue) error {
	//: a startup check latches: once it passes it has answered for good.
	return h.add(&entry{
		name: check.Name, probe: corehealth.ProbeStartup, latching: true,
		budget: h.cfg.checkBudget(check.Timeout), run: check.Check,
	}, check.Check == nil)
}

// AddReadiness registers a check that gates routing.
func (h *health) AddReadiness(check corehealth.ReadinessCheckValue) error {
	//: refuse a window the SDK will not stand behind, rather than clamping it
	//: and answering a question the caller did not ask (see MaxCacheAge).
	if check.MaxAge > MaxCacheAge {
		//: the fields carry both numbers so the fix needs no lookup.
		return kerrs.Wrap(StaleCacheWindow, kerrs.WrapParams{},
			kerrs.String("check", check.Name),
			kerrs.String("max_age", check.MaxAge.String()),
			kerrs.String("ceiling", MaxCacheAge.String()))
	}
	//: readiness is the only registration that carries criticality and a cache
	//: window, so it is the only one that copies them onto the entry.
	return h.add(&entry{
		name: check.Name, probe: corehealth.ProbeReadiness,
		budget: h.cfg.checkBudget(check.Timeout), run: check.Check,
		nonCritical: check.NonCritical, maxAge: check.MaxAge,
	}, check.Check == nil)
}

// AddLiveness registers process-local evidence that the process is not
// irrecoverable.
func (h *health) AddLiveness(check corehealth.LivenessCheckValue) error {
	//: no nonCritical and no maxAge: LivenessCheckValue does not carry
	//: either, and the absences are the decisions — see its doc comment.
	return h.add(&entry{
		name: check.Name, probe: corehealth.ProbeLiveness,
		budget: h.cfg.checkBudget(check.Timeout), runSelf: check.Check,
	}, check.Check == nil)
}

// add is the shared registration path. bodyMissing is passed in because the
// three registrations carry two different function types, and a nil check on
// an interface-typed field would be a different question from a nil check on
// each concrete one.
func (h *health) add(e *entry, bodyMissing bool) error {
	//: a nameless check cannot be reported on, and its results would be
	//: indistinguishable from any other nameless check's.
	if e.name == "" {
		//: the field says which part is missing.
		return kerrs.Wrap(corehealth.InvalidCheck, kerrs.WrapParams{},
			kerrs.String("missing", "Name"), kerrs.String("probe", e.probe.String()))
	}
	//: a nil body would panic on the first probe — the one request whose
	//: failure an operator is least able to rehearse.
	if bodyMissing {
		//: refuse at registration, where the caller can still fix it.
		return kerrs.Wrap(corehealth.InvalidCheck, kerrs.WrapParams{},
			kerrs.String("missing", "Check"), kerrs.String("check", e.name),
			kerrs.String("probe", e.probe.String()))
	}
	//: named and runnable; what is left is the per-probe namespace.
	return h.register(e)
}

// register installs a validated entry, refusing a name already taken on that
// probe.
func (h *health) register(e *entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	//: the namespace is per probe, not global; see health.names.
	if h.names[e.probe] == nil {
		h.names[e.probe] = map[string]bool{}
	}
	//: a duplicate name would make every result and every error ambiguous.
	if h.names[e.probe][e.name] {
		//: refuse rather than shadowing the first registration.
		return kerrs.Wrap(corehealth.DuplicateCheck, kerrs.WrapParams{},
			kerrs.String("check", e.name), kerrs.String("probe", e.probe.String()))
	}
	h.names[e.probe][e.name] = true
	h.checks[e.probe] = append(h.checks[e.probe], e)
	//: a startup check registered is one more thing standing between this
	//: process and "serving".
	if e.latching {
		h.startupPending++
	}
	//: registered at the end of the order.
	return nil
}

// Drain marks the process as going away. Readiness reports not-ready from here
// on without running a check; liveness keeps answering normally.
//
// It is one-way by construction: there is no field to clear and no method to
// clear it. A process that has announced its departure has already had its
// endpoints withdrawn, and a flap would send live traffic back to a process
// that is closing the dependencies that traffic needs.
func (h *health) Drain() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.draining = true
}

// startupPassed records that one startup check latched.
func (h *health) startupPassed() {
	h.mu.Lock()
	defer h.mu.Unlock()
	//: never below zero: finish reports a latch exactly once per check.
	if h.startupPending > 0 {
		h.startupPending--
	}
}

// entriesFor returns the registered checks for one probe, in registration
// order, as a snapshot the probe can read without holding the lock.
func (h *health) entriesFor(probe corehealth.Probe) []*entry {
	//: a read lock: this reads the registration set and copies it out.
	h.mu.RLock()
	defer h.mu.RUnlock()
	//: the slice is copied; the entries themselves carry their own mutex and
	//: are shared on purpose — that is what makes the cache and the
	//: single-flight run outlive one probe.
	return slices.Clone(h.checks[probe])
}
