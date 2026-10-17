package app

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"time"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/observe/logger"
)

// The running app as the Studio reads it (ADR 0180 §4, step 1): the
// Studio's routes, their guard and their handlers are framework/app/studio's
// (studioengine), and read the app through studioHost, which only EnableStudio
// builds — so a program that does not import framework/app/studio links
// none of what is below, nor what it alone reaches: the source endpoint's
// reads, the dev tools' and the store tools' views. What it reads stays in
// the core, beside the state it reads, and names no SDK type of the Studio.

// studioHost is an app, as the Studio's routes read it.
type studioHost struct {
	a *App
}

// Graph is the app's graph.
func (h studioHost) Graph() *model.Graph { return h.a.Graph() }

// Phase is the app's phase.
func (h studioHost) Phase() string { return h.a.currentPhase() }

// Now is the time on the app's clock.
func (h studioHost) Now() time.Time { return h.a.clock.Now() }

// Subscribe streams the app's live events.
func (h studioHost) Subscribe() (events <-chan model.Event, cancel func()) {
	return h.a.hub.subscribe()
}

// Stopping is closed when the app ends its live streams.
func (h studioHost) Stopping() <-chan struct{} { return h.a.streams.Done() }

// Traces are the latest traces the hub keeps.
func (h studioHost) Traces(limit int, root, node string) []model.Trace {
	return h.a.hub.recentTraces(limit, root, node)
}

// Trace is one trace, while the hub keeps it.
func (h studioHost) Trace(id string) (model.Trace, bool) { return h.a.hub.trace(id) }

// Logs are the latest log records the hub keeps (logs.go).
func (h studioHost) Logs(trace, node, level, limit string) ([]model.LogRecord, error) {
	return h.a.studioLogs(trace, node, level, limit)
}

// Instances are a workflow's entities and where each is.
func (h studioHost) Instances(workflow string) ([]model.Instance, bool) {
	wf, ok := h.a.findNode(workflow).(interface{ instanceList() []model.Instance })
	if !ok {
		return nil, false
	}
	return wf.instanceList(), true
}

// Items are a store's latest entities, each redacted as a payload is.
func (h studioHost) Items(store string) ([]json.RawMessage, bool) {
	st, ok := h.a.findNode(store).(itemsSource)
	if !ok {
		return nil, false
	}
	items := st.items(200)
	out := make([]json.RawMessage, len(items))
	for i, raw := range items {
		out[i] = st.redacted(raw)
	}
	return out, true
}

// Source is a file the graph names, read through an os.Root confined to
// the file's root: the product's module, or, with module, another Go module
// of the build whose root a position revealed — a mounted module's, a
// library's. A regular file only, never a link at its name, even one put
// there while it is read (readSource); a link on its path is followed inside
// the root only, as os.Root does.
func (h studioHost) Source(ctx context.Context, module, file string) ([]byte, bool) {
	a := h.a
	if file == "" || !filepath.IsLocal(filepath.FromSlash(file)) || !slices.Contains(a.Graph().Files(), model.File{GoModule: module, File: file}) {
		return nil, false
	}
	root, err := a.openSourceRoot(module)
	if err != nil {
		return nil, false
	}
	defer func() {
		if err := root.Close(); err != nil {
			logger.Debug(ctx, a.log, "the source root did not close cleanly", logger.String("error", err.Error()))
		}
	}()
	raw, err := readSource(root, filepath.FromSlash(file))
	if err != nil {
		return nil, false
	}
	return raw, true
}

// Locate is moduleSource for the profiler (profile.go).
func (h studioHost) Locate(file string, line int, fn string) *model.Source {
	return h.a.sourceOf(file, line, fn)
}

// Mails are every mailer's latest mails, at most limit of each.
func (h studioHost) Mails(limit int) []model.MailSummary {
	out := make([]model.MailSummary, 0, limit)
	for _, mb := range h.a.mailboxes() {
		out = append(out, mb.mails(limit)...)
	}
	return out
}

// Mail is one mail a mailer remembers.
func (h studioHost) Mail(id string) (model.MailMessage, bool) {
	for _, mb := range h.a.mailboxes() {
		if m, ok := mb.mail(id); ok {
			return m, true
		}
	}
	return model.MailMessage{}, false
}

// Stores reports whether kit's own store code is linked: without
// framework/app/store there is no store to read.
func (h studioHost) Stores() bool { return stores() != nil }

// Databases are the app's databases, sampled now (database_view.go).
func (h studioHost) Databases(ctx context.Context) []model.Database {
	return h.a.studioDatabases(ctx)
}

// Former are a record's former values (history_view.go).
func (h studioHost) Former(ctx context.Context, store, key string) (model.RecordHistory, error) {
	return h.a.studioFormer(ctx, store, key)
}

// Revisions are a record's versions (revisions_view.go).
func (h studioHost) Revisions(ctx context.Context, store, key, from, to string) (model.RecordVersions, error) {
	return h.a.studioRevisions(ctx, store, key, from, to)
}

// Trash is a page of a store's trash (trash_view.go).
func (h studioHost) Trash(ctx context.Context, store, offset, limit string) (model.StoreTrash, error) {
	return h.a.studioTrash(ctx, store, offset, limit)
}

// Privacy is the Privacy page (privacyapi.go).
func (h studioHost) Privacy(ctx context.Context) (model.Privacy, error) {
	return h.a.privacyView(ctx)
}

// Remote reports whether the Studio answers a client not on this machine.
func (h studioHost) Remote() bool { return h.a.cfg.studio.remote }

// AllowedHost reports whether host is a loopback name or one the operator
// allows.
func (h studioHost) AllowedHost(host string) bool {
	return slices.Contains(h.a.cfg.allowedHosts, host)
}

// Reply writes body, or err as kit answers an error.
func (h studioHost) Reply(w http.ResponseWriter, r *http.Request, body json.RawMessage, err error) {
	if err != nil {
		h.a.replyError(r.Context(), w, err)
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// Forbid answers 403 with kit's forbidden code.
func (h studioHost) Forbid(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusForbidden, wireError{Error: wireBody{Code: WireForbidden, Message: message}})
}

// Fail answers 500 with kit's internal code, saying message.
func (h studioHost) Fail(w http.ResponseWriter, message string) {
	writeJSON(w, http.StatusInternalServerError, wireError{Error: wireBody{Code: WireInternal, Message: message}})
}

// NotFound is kit's not found.
func (h studioHost) NotFound(message string) error { return NotFound(message) }

// Invalid is kit's invalid request.
func (h studioHost) Invalid(message string) error { return Invalid(message) }

// ProfileUnreadable is CodeProfileRead's refusal.
func (h studioHost) ProfileUnreadable(public string, cause error) error {
	return failure(CodeProfileRead, "PROFILE_UNREADABLE", public, cause)
}

// Debug logs a failure nobody is told of.
func (h studioHost) Debug(ctx context.Context, message string, err error) {
	logger.Debug(ctx, h.a.log, message, logger.String("error", err.Error()))
}
