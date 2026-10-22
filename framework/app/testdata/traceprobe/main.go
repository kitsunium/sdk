// Command traceprobe is a whole product on framework/app, every subsystem
// but the Studio imported: a store holding a personal field, a frontend
// served from embedded files, a topic and its subscription, a workflow, a
// mailer, two jobs, a command and a query exposed over HTTP, a webhook, a
// setting and a secret, and an OTLP export, off until its endpoint is set. framework/app's guard builds it as a release is
// built and reads its sources, its binary and what it says when it runs.
package main

import (
	"context"
	"embed"
	"os"
	"time"

	"github.com/kitsunium/sdk/framework/app"
	_ "github.com/kitsunium/sdk/framework/app/jobs"
	_ "github.com/kitsunium/sdk/framework/app/mail"
	_ "github.com/kitsunium/sdk/framework/app/otlp"
	_ "github.com/kitsunium/sdk/framework/app/queue"
	_ "github.com/kitsunium/sdk/framework/app/server"
	_ "github.com/kitsunium/sdk/framework/app/store"
	_ "github.com/kitsunium/sdk/framework/app/workflow"
)

// Status is where an order stands.
type Status string

// The states of an order.
const (
	Placed  Status = "placed"
	Shipped Status = "shipped"
)

// Order is what the shop keeps: its buyer's address is personal data, and
// says whom the order is about.
type Order struct {
	ID       string    `json:"id"`
	Email    string    `json:"email" app:"personal,subject"`
	Status   Status    `json:"status"`
	PlacedAt time.Time `json:"placedAt"`
}

// placedAt is when an order's retention starts.
func placedAt(o Order) (time.Time, bool) { return o.PlacedAt, !o.PlacedAt.IsZero() }

// ShippedEvent is what the shop announces when an order leaves.
type ShippedEvent struct {
	ID string `json:"id"`
}

// ByID names an order.
type ByID struct {
	ID string `json:"id" path:"id"`
}

// Hook is what a carrier posts when a parcel moves.
type Hook struct {
	ID string `json:"id"`
}

//go:embed site
var site embed.FS

// Shop is the product's only service.
var Shop = app.NewService("shop", "Takes and ships orders.")

// Orders holds the orders.
var Orders = Shop.Store("orders", func(o Order) string { return o.ID },
	app.EraseAfter(365*24*time.Hour, placedAt))

// Lifecycle moves an order from placed to shipped.
var Lifecycle = Shop.Workflow("lifecycle", Orders, func(o *Order) *Status { return &o.Status }).
	Initial(Placed).
	On("ship", Placed, Shipped)

// Departures announces the orders that left.
var Departures = Shop.Topic[ShippedEvent]("departures")

// Receipts mails the buyer when an order leaves.
var Receipts = Shop.Mailer("receipts", app.From("Shop", "shop@example.com"))

// Greeting is a setting the operator may change.
var Greeting = Shop.Setting("greeting", "hello")

// CarrierToken is what the carrier signs its hooks with.
var CarrierToken = Shop.Secret("carrier-token", app.Optional())

var _ = Shop.Subscribe("receipt", Departures, func(context.Context, ShippedEvent) error { return nil })

var _ = Shop.Command("ship", func(ctx context.Context, in ByID) (Order, error) {
	return Lifecycle.Fire(ctx, in.ID, "ship")
}).Expose("POST /orders/{id}/ship", app.Anyone())

var _ = Shop.Query("order", func(ctx context.Context, in ByID) (Order, error) {
	return Orders.Get(ctx, in.ID)
}).Expose("GET /orders/{id}", app.Anyone())

var _ = Shop.Endpoint("POST /hooks/carrier", func(context.Context, Hook) (struct{}, error) {
	return struct{}{}, nil
})

var _ = Shop.Static("site", "/", site, app.Root("site"))

var _ = Shop.Every("sweep", time.Hour, func(context.Context) error { return nil })

var _ = Shop.Cron("report", "0 3 * * *", func(context.Context) error { return nil })

// App is the product.
var App = app.NewApp("shop", Shop).With(app.OTLP())

func main() { os.Exit(App.Main(context.Background(), os.Args[1:])) }
