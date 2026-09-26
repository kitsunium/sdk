# statemachine (core)

The state-machine **domain** contract (ADR 0120): the `Store[E]` port a machine
reads and writes the caller's entities through, the `Journal[S]` port it keeps
its per-entity records in, and the values they speak.

```go
type orders struct{ /* your collection */ }

func (o *orders) Key(order Order) string                                  { return order.ID }
func (o *orders) Get(ctx context.Context, key string) (Order, bool, error) { … }  // found == false: none
func (o *orders) Insert(ctx context.Context, order Order) (bool, error)     { … }  // false: key taken
func (o *orders) Replace(ctx context.Context, order Order) (bool, error)    { … }  // false: gone — never resurrect
func (o *orders) All(ctx context.Context) iter.Seq2[Order, error]          { … }
```

`Store` is frozen at five methods, `Journal` at three (ADR 0039). A
`RecordValue` is an entity's state, since when, and its latest `StepValue`s;
a `Trigger` — start, event, delay, deadline, guard — says what fired a step,
with stable values and `ParseTrigger` for its name. `TriggerUnknown`
(`0.2.56.1`) refuses a name that is none of the five.

The engine — declarations, per-entity locks, hooks, the agenda and the loop —
is `internal/service/statemachine`. Public facade: `pkg/v1/statemachine`. See
`CLAUDE.md`.
