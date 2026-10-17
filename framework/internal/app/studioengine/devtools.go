package studioengine

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"

	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/framework/model"
)

// What the dev tools read, in dev: the process, the heap profile, goroutines,
// logs, the mailbox, a field's former values, a record's versions, a store's
// trash, the databases and the Privacy page. mount puts every route here
// behind the Studio's guard; none of them acts on the product (D13, ADR 0147
// §8).

// maxMails bounds the mailbox list.
const maxMails int = 1000

// defaultMails is how many mails the mailbox lists when the Studio does not
// say.
const defaultMails int = 200

// mountDevTools registers the dev tools' routes through api, which puts each
// behind the Studio's guard.
func (s studio) mountDevTools(api func(pattern string, h http.HandlerFunc)) {
	api("GET /_kit/api/process", s.serveProcess)
	api("GET /_kit/api/goroutines", s.serveGoroutines)
	api("GET /_kit/api/profile/heap", s.serveHeapProfile)
	api("GET /_kit/api/logs", s.serveLogs)
	api("GET /_kit/api/mail", s.serveMails)
	api("GET /_kit/api/mail/{id}", s.serveMail)
	s.mountStoreTools(api)
}

// mountStoreTools registers the dev tools that read the stores and the
// databases: the databases, a field's former values, a record's versions, a
// store's trash, and the Privacy page. They are served only with the store
// subsystem — without it there is no store to read.
func (s studio) mountStoreTools(api func(pattern string, h http.HandlerFunc)) {
	if !s.host.Stores() {
		return
	}
	api("GET /_kit/api/databases", func(w http.ResponseWriter, r *http.Request) {
		answer(s, w, r, s.host.Databases(r.Context()), nil)
	})
	api("GET /_kit/api/former", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.host.Former(r.Context(), q.Get("store"), q.Get("key"))
		answer(s, w, r, out, err)
	})
	api("GET /_kit/api/revisions", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.host.Revisions(r.Context(), q.Get("store"), q.Get("key"), q.Get("from"), q.Get("to"))
		answer(s, w, r, out, err)
	})
	api("GET /_kit/api/trash", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		out, err := s.host.Trash(r.Context(), q.Get("store"), q.Get("offset"), q.Get("limit"))
		answer(s, w, r, out, err)
	})
	// The Privacy page (ADR 0006 §9): the register and its gaps, the holds,
	// the journal's latest entries and whether its chain holds. Running a
	// retention, exporting and erasing a person are the product's commands,
	// never an HTTP route; a journal or holds kit cannot read are an error,
	// never an empty page.
	api("GET /_kit/api/privacy", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.host.Privacy(r.Context())
		answer(s, w, r, out, err)
	})
}

// serveProcess answers GET /_kit/api/process: a fresh sample, for polling;
// 404 when no sampler is plugged in — a product that did not import
// framework/app/server, which serves no route anyway.
func (s studio) serveProcess(w http.ResponseWriter, r *http.Request) {
	sample := plug.SampleProcess.Load()
	if sample == nil {
		http.NotFound(w, r)
		return
	}
	p := (*sample)()
	if p == nil {
		http.NotFound(w, r)
		return
	}
	answer(s, w, r, p, nil)
}

// serveHeapProfile answers GET /_kit/api/profile/heap: the live heap, after
// a collection, folded onto the graph.
func (s studio) serveHeapProfile(w http.ResponseWriter, r *http.Request) {
	out, err := heap(s.host.Graph(), s.host.Locate)
	if err != nil {
		s.host.Reply(w, r, nil, s.host.ProfileUnreadable("the heap profile could not be read", err))
		return
	}
	out.At = s.host.Now().UTC()
	answer(s, w, r, out, nil)
}

// serveGoroutines answers GET /_kit/api/goroutines: every goroutine, grouped
// by the node it works for, the loop it belongs to, what it waits on, and
// its innermost frame that is not the runtime's.
func (s studio) serveGoroutines(w http.ResponseWriter, r *http.Request) {
	out, err := goroutines(s.host.Now().UTC())
	if err != nil {
		s.host.Reply(w, r, nil, s.host.ProfileUnreadable("the goroutines could not be listed", err))
		return
	}
	answer(s, w, r, out, nil)
}

// serveLogs answers GET /_kit/api/logs?trace=&node=&level=&limit=.
func (s studio) serveLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := s.host.Logs(q.Get("trace"), q.Get("node"), q.Get("level"), q.Get("limit"))
	answer(s, w, r, out, err)
}

// serveMails answers GET /_kit/api/mail?limit=: every mailer's mail,
// newest first.
func (s studio) serveMails(w http.ResponseWriter, r *http.Request) {
	limit := defaultMails
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxMails {
			s.host.Reply(w, r, nil, s.host.Invalid("limit must be a number from 1 to "+strconv.Itoa(maxMails)))
			return
		}
		limit = n
	}
	out := s.host.Mails(limit)
	slices.SortStableFunc(out, func(x, y model.MailSummary) int {
		return cmp.Or(y.QueuedAt.Compare(x.QueuedAt), cmp.Compare(y.ID, x.ID))
	})
	answer(s, w, r, out[:min(limit, len(out))], nil)
}

// serveMail answers GET /_kit/api/mail/{id}: one whole mail.
func (s studio) serveMail(w http.ResponseWriter, r *http.Request) {
	m, ok := s.host.Mail(r.PathValue("id"))
	if !ok {
		s.host.Reply(w, r, nil, s.host.NotFound("no such mail, or it left the mailbox"))
		return
	}
	answer(s, w, r, m, nil)
}
