package plug

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/kitsunium/sdk/framework/model"
)

// The Studio's contract (ADR 0180 §4, step 1): the running app as the
// Studio's routes read it, which kit's core implements and
// framework/internal/app/studioengine reads. Every method reads; none acts on
// the product (D13). A query parameter the app judges against what it
// keeps — a log level, a page of the trash, two version numbers — is handed
// as the Studio received it, so that the answer to a request is the one kit
// gave before the routes left its core.

// StudioHost is the running app, as the Studio's routes read it and answer
// through it.
type StudioHost interface {
	// Graph is the app's graph, as it is now.
	Graph() *model.Graph
	// Phase is where the app is in its life, as the graph says it.
	Phase() string
	// Now is the time on the app's clock.
	Now() time.Time
	// Subscribe streams the app's live events until cancel is called.
	Subscribe() (events <-chan model.Event, cancel func())
	// Stopping is closed when the app ends its live streams.
	Stopping() <-chan struct{}
	// Traces are the latest traces the app keeps, at most limit, those of
	// root's trace or through node when either is given.
	Traces(limit int, root, node string) []model.Trace
	// Trace is one trace, while the app keeps it.
	Trace(id string) (model.Trace, bool)
	// Logs are the latest records the product logged, those of a trace or
	// a node and from a level when given, at most limit — a default when
	// it is empty; a level or a limit the app does not take is refused.
	Logs(trace, node, level, limit string) ([]model.LogRecord, error)
	// Instances are a workflow's entities and where each is; false for no
	// such workflow.
	Instances(workflow string) ([]model.Instance, bool)
	// Items are a store's latest entities, redacted as payloads are; false
	// for no such store.
	Items(store string) ([]json.RawMessage, bool)
	// Source is a file the graph names, of the product's Go module or of
	// the one module names: a regular file within the bound, never a link;
	// false for any other name.
	Source(ctx context.Context, module, file string) ([]byte, bool)
	// Locate says where the function fn, at file:line, lies in the
	// product's own module; nil anywhere else.
	Locate(file string, line int, fn string) *model.Source
	// Mails are every mailer's latest mails, at most limit of each.
	Mails(limit int) []model.MailSummary
	// Mail is one mail, whole when its mailer keeps bodies; false when no
	// mailer remembers it.
	Mail(id string) (model.MailMessage, bool)
	// Stores reports whether the app runs on the store subsystem: the
	// Studio's routes on the stores and the databases are mounted only then.
	Stores() bool
	// Databases are the app's databases, each checked and its migrations
	// read again now.
	Databases(ctx context.Context) []model.Database
	// Former are the former values of the record under key in store, as
	// the Studio's data view shows them.
	Former(ctx context.Context, store, key string) (model.RecordHistory, error)
	// Revisions are the versions of the record under key in store, and
	// what changed from version from to version to when both are given.
	Revisions(ctx context.Context, store, key, from, to string) (model.RecordVersions, error)
	// Trash is a page of store's trash, from offset and at most limit
	// records — defaults when they are empty.
	Trash(ctx context.Context, store, offset, limit string) (model.StoreTrash, error)
	// Privacy is what the Studio's Privacy page shows.
	Privacy(ctx context.Context) (model.Privacy, error)
	// Remote reports whether the Studio answers a client that is not on
	// this machine.
	Remote() bool
	// AllowedHost reports whether a request's Host, its port, brackets and
	// case removed, may reach the Studio.
	AllowedHost(host string) bool
	// Reply answers r with body, a JSON document, or with err as the framework
	// answers an error.
	Reply(w http.ResponseWriter, r *http.Request, body json.RawMessage, err error)
	// Forbid answers that the caller may not reach the Studio, saying
	// message.
	Forbid(w http.ResponseWriter, message string)
	// Fail answers an internal error, saying message and nothing of the
	// error.
	Fail(w http.ResponseWriter, message string)
	// NotFound is the framework's answer to a request for what does not exist,
	// saying message.
	NotFound(message string) error
	// Invalid is the framework's answer to a request the caller must change, saying
	// message.
	Invalid(message string) error
	// ProfileUnreadable is the framework's refusal of a profile the process could
	// not read: public says which, cause why, for the logs.
	ProfileUnreadable(public string, cause error) error
	// Debug logs, at the debug level, a failure nobody is told of.
	Debug(ctx context.Context, message string, err error)
}
