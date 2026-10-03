// Package kit — the catalog of the generic mechanics a node is built from.
package kit

import (
	"fmt"
	"slices"
	"sync"

	"github.com/kitsunium/sdk/framework/model"
	"github.com/kitsunium/sdk/pkg/v1/app/id"
)

// Catalog lists the generic mechanics kit offers: the building blocks a node
// composes rather than implements, each one backed by an SDK package. It is
// part of every graph, so the Studio — and an AI agent reading the graph —
// knows what can be added to a product and the exact code that adds it.
func Catalog() []model.Mechanic {
	return slices.Concat(requestMechanics(), operationMechanics(), backgroundMechanics())
}

// requestMechanics are what a request goes through before its handler — a
// command's and a query's too.
func requestMechanics() []model.Mechanic {
	return []model.Mechanic{
		{
			Kind: "auth", Label: "authentication",
			Doc:     "Runs the app's auth handler before anything else: a request without valid credentials is answered 401, and the handler reads the caller with kit.UserID.",
			Snippet: "kit.Auth() // or kit.AuthOptional(), with one Service.AuthHandler in the app",
		},
		{
			Kind: "validate", Label: "validate", Package: "github.com/kitsunium/sdk/pkg/v1/app/validation",
			Doc:     "Checks a request against the validate tags of its type before the handler runs; every violation is reported with its path, never with its value.",
			Snippet: "Title string `json:\"title\" validate:\"required,maxlen=200\"`",
		},
		{
			Kind: "ratelimit", Label: "rate limit", Package: "github.com/kitsunium/sdk/pkg/v1/app/resilience",
			Doc:     "A token bucket in front of an endpoint's handler; an empty bucket answers 429 at once. RateLimitPerClient gives each user, or each address, a bucket of its own.",
			Snippet: "kit.RateLimitPerClient(2, 5)",
		},
		{
			Kind: "timeout", Label: "timeout", Package: "github.com/kitsunium/sdk/pkg/v1/app/resilience",
			Doc:     "Cancels the handler's context after a deadline and answers 504.",
			Snippet: "kit.Timeout(2 * time.Second)",
		},
		{
			Kind: "bulkhead", Label: "bulkhead", Package: "github.com/kitsunium/sdk/pkg/v1/app/resilience",
			Doc:     "Caps how many requests an endpoint serves at once; the overflow answers 503.",
			Snippet: "kit.Bulkhead(8)",
		},
	}
}

// operationMechanics are a command's and a query's own (ADR 0005).
func operationMechanics() []model.Mechanic {
	return []model.Mechanic{
		{
			Kind: "authorize", Label: "authorization", Package: "github.com/kitsunium/sdk/pkg/v1/security/authz",
			Doc:     "Checks, last before a command's or a query's handler, that the caller may run it: a permission as data, which the SDK's authz grants from the auth data's attributes (kit.Principal), and a rule that needs the data. Every refusal is the same 403.",
			Snippet: "Service.Command(\"place-order\", placeOrder).Allow(Policy, \"place\", \"order\").Authorize(ownsOrder)",
		},
		{
			Kind: "key", Label: "key", Package: "github.com/kitsunium/sdk/pkg/v1/app/lock",
			Doc:     "Names the entity a command is about: two runs with one key never overlap, and a queued command whose key waits or runs is not queued again. The SDK's lock, in the process.",
			Snippet: "Service.Command(\"cancel-order\", cancelOrder).Key(func(in ByID) string { return in.ID })",
		},
		{
			Kind: "transaction", Label: "transaction", Package: "github.com/kitsunium/sdk/pkg/v1/data/sql",
			Doc:     "Runs a command's authorization and handler in one transaction, opened once its key is held: on a database the database's own, on the data directory and in memory kit's, which takes the writer turn. A failed command changes nothing; what it publishes, mails or dispatches leaves at the commit. kit.Transact opens one anywhere; kit.NoTransaction opts a command out.",
			Snippet: "err := kit.Transact(ctx, func(ctx context.Context) error { return Orders.Insert(ctx, o) })",
		},
		{
			Kind: "queue", Label: "queued command", Package: "github.com/kitsunium/sdk/pkg/v1/data/queue",
			Doc:     "Sends a command to the background: Dispatch returns once its own queue accepted it, and a consumer handles it as the user who dispatched it, retried, then dead-lettered. Exposed, it answers 202.",
			Snippet: "Service.Command(\"reindex\", reindex, kit.Queued(), kit.MaxDeliveries(3))",
		},
	}
}

// backgroundMechanics are what runs apart from a caller: a queue's retries,
// a schedule — and the identifiers every one of them mints.
func backgroundMechanics() []model.Mechanic {
	return []model.Mechanic{
		{
			Kind: "retry", Label: "retry + dead letter", Package: "github.com/kitsunium/sdk/pkg/v1/data/queue",
			Doc:     "A subscription redelivers a failed message up to MaxDeliveries times, then moves it to its dead-letter store.",
			Snippet: "kit.MaxDeliveries(5)",
		},
		{
			Kind: "schedule", Label: "schedule", Package: "github.com/kitsunium/sdk/pkg/v1/app/scheduler",
			Doc:     "Runs a job at a fixed interval or on a cron expression; an overlapping or missed fire is skipped and counted.",
			Snippet: "Service.Every(\"sample\", 10*time.Second, Sample)",
		},
		{
			Kind: "id", Label: "typed id", Package: "github.com/kitsunium/sdk/pkg/v1/app/id",
			Doc:     "Time-ordered, prefixed identifiers (TypeID over UUIDv7).",
			Snippet: "kit.NewID(\"todo\")",
		},
	}
}

var idGenerators sync.Map // prefix → id.Generator

// NewID returns a new identifier with the given prefix: a TypeID, time
// ordered, like "todo_01k5zq7m3xe8tvbfg0s7zr4w6c". The prefix must be one to
// sixty-three lower-case letters, with '_' only between two letters; any
// other prefix is a programming error and panics.
func NewID(prefix string) string {
	g, ok := idGenerators.Load(prefix)
	if !ok {
		gen, err := id.NewTypeID(prefix)
		if err != nil {
			panic(fmt.Sprintf("kit.NewID: %q is not a TypeID prefix: use lower-case letters", prefix))
		}
		g, _ = idGenerators.LoadOrStore(prefix, gen)
	}
	gen, isGenerator := g.(id.Generator)
	if !isGenerator {
		panic(fmt.Sprintf("kit.NewID: the generator of %q is not one", prefix))
	}
	s, err := gen.New()
	if err != nil {
		panic(fmt.Sprintf("kit.NewID: the system's random source failed: %v", err))
	}
	return s
}
