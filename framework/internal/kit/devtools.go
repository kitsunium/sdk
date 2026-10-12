package kit

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// maxMails bounds the mailbox list.
const maxMails int = 1000

// defaultMails is how many mails the mailbox lists when the Studio does not
// say.
const defaultMails int = 200

// What the dev tools read, in dev: the process, the heap profile, goroutines,
// logs, the mailbox, a field's former values. mountIntrospection mounts every
// route here behind hostGuard; none of them exists outside dev (invariant 4),
// and none of them acts on the product (D13, ADR 0147 §8): no mock, no fault,
// no run, no fire, no dispatch, no ask, no wake, no CPU capture.

// mountDevTools registers the dev tools' routes through api, which puts each
// behind the Studio's guard.
func (a *App) mountDevTools(api func(pattern string, h http.HandlerFunc)) {
	api("GET /_kit/api/process", a.serveProcess)
	api("GET /_kit/api/databases", a.serveDatabases)
	api("GET /_kit/api/goroutines", a.serveGoroutines)
	api("GET /_kit/api/profile/heap", a.serveHeapProfile)
	api("GET /_kit/api/logs", a.serveLogs)
	api("GET /_kit/api/mail", a.serveMails)
	api("GET /_kit/api/mail/{id}", a.serveMail)
	api("GET /_kit/api/former", a.serveFormer)
	api("GET /_kit/api/revisions", a.serveRevisions)
	a.mountPrivacyTools(api)
}

// goTracked runs fn on a goroutine Stop waits for (invariant 9). It refuses,
// and reports false, once Stop has begun.
func (a *App) goTracked(fn func()) bool {
	a.mu.Lock()
	if a.stopping {
		a.mu.Unlock()
		return false
	}
	a.wg.Add(1)
	a.mu.Unlock()
	go func() {
		defer a.wg.Done()
		fn()
	}()
	return true
}

// The process ---------------------------------------------------------------

// serveProcess answers GET /_kit/api/process: a fresh sample, for polling;
// 404 when no sampler is plugged in — a product that did not import
// framework/kit/server, which serves no route anyway.
func (a *App) serveProcess(w http.ResponseWriter, r *http.Request) {
	p := processSample()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// processSample is a sample of the running process from the sampler
// framework/kit/server plugs in (plug.SampleProcess), nil without one: kit
// itself imports neither runtime/metrics nor the SDK's process package, whose
// initialisation a CLI or a daemon would pay for nothing (ADR 0172).
func processSample() *model.Process {
	sample := plug.SampleProcess.Load()
	if sample == nil {
		return nil
	}
	return (*sample)()
}

// Controls ------------------------------------------------------------------

// The mailbox ---------------------------------------------------------------

// mailboxes returns every node that keeps mail.
func (a *App) mailboxes() []mailbox {
	var out []mailbox
	for _, svc := range a.services {
		if svc == nil {
			continue
		}
		nodes, _ := svc.snapshot()
		for _, n := range nodes {
			if mb, ok := n.(mailbox); ok {
				out = append(out, mb)
			}
		}
	}
	return out
}

// serveMails answers GET /_kit/api/mail?limit=: every mailer's mail,
// newest first.
func (a *App) serveMails(w http.ResponseWriter, r *http.Request) {
	limit := defaultMails
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMails {
			a.replyError(r.Context(), w, Invalid("limit must be a number from 1 to "+strconv.Itoa(maxMails)))
			return
		}
		limit = n
	}
	out := make([]model.MailSummary, 0, limit)
	for _, mb := range a.mailboxes() {
		out = append(out, mb.mails(limit)...)
	}
	slices.SortStableFunc(out, func(x, y model.MailSummary) int {
		return cmp.Or(y.QueuedAt.Compare(x.QueuedAt), cmp.Compare(y.ID, x.ID))
	})
	writeJSON(w, http.StatusOK, out[:min(limit, len(out))])
}

// serveMail answers GET /_kit/api/mail/{id}: one whole mail.
func (a *App) serveMail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, mb := range a.mailboxes() {
		if m, ok := mb.mail(id); ok {
			writeJSON(w, http.StatusOK, m)
			return
		}
	}
	a.replyError(r.Context(), w, NotFound("no such mail, or it left the mailbox"))
}
