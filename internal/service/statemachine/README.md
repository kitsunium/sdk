# statemachine (service)

The state-machine engine over stored entities (ADR 0120). Declare, then run:

```go
spec := statemachine.NewMachineSpec(func(o *Order) *Status { return &o.Status }).
    Initial(Pending).
    On("pay", Pending, Paid).                                   // a caller fires it
    After("abandon", 24*time.Hour, Pending, Cancelled).         // a duration in the state
    At("expire", Paid, Expired, shipBy).                        // an instant the entity carries
    When("ready", Paid, Shipping, packed)                       // a guard, asked on each write

m, err := statemachine.NewStateMachine(ctx, spec, &statemachine.Config[Order, Status]{Store: orders})
go m.Run(ctx)
o, err := m.Fire(ctx, "o-1", "pay")
```

Transitions of one entity run one at a time; a hook that panics never leaves
an entity locked. The loop keeps an agenda — one heap entry per entity — and
sleeps until the next transition due or a write: O(log N) to the next due where
re-reading the store is O(N) (see `BENCH.md`).

| Code | Reason | When |
|---|---|---|
| `0.3.88.1` | `TRANSITION_REFUSED` | no event transition by that name leaves the state (409) |
| `0.3.88.2` | `ENTITY_MISSING` | no entity under the key, or deleted while the transition ran (404) |
| `0.3.88.3` | `ENTITY_EXISTS` | `Start` over a key already taken (409) |
| `0.3.88.4` | `KEY_EMPTY` | `Start` with an entity whose key is empty |
| `0.3.88.5` | `HOOK_FAILED` | a hook returned an error, joined beside it |
| `0.3.88.6` | `HOOK_PANICKED` | a hook panicked; value and stack as fields |
| `0.3.88.7` | `HOOK_CHANGED_STATE` | an OnEnter hook changed the state it was entering |
| `0.3.88.8` | `HOOK_CHANGED_KEY` | an OnEnter hook changed the entity's key |
| `0.3.88.9` | `REENTRANT` | an OnEnter hook fired its own machine |
| `0.3.88.10` | `STORE_FAILED` | a `Store` method returned an error |
| `0.3.88.11` | `JOURNAL_FAILED` | a `Journal` method returned an error |
| `0.3.88.12` | `LOOP_RUNNING` | `Run` or `Step` while another is going |
| `0.3.88.13` | `FUNCTION_PANICKED` | a guard or an instant function panicked |
| `0.3.88.14` | `INITIAL_MISSING` | the declaration never called `Initial` |
| `0.3.88.15` | `EVENT_INVALID` | an empty event name, or `create` |
| `0.3.88.16` | `TRANSITION_DUPLICATE` | one event declared twice from one state |
| `0.3.88.17` | `DELAY_INVALID` | `After` with a duration that is not positive |
| `0.3.88.18` | `FUNCTION_MISSING` | a nil accessor, guard, instant function, hook or definition |
| `0.3.88.19` | `STORE_MISSING` | no `Config.Store` |
| `0.3.88.20` | `WAIT_ABANDONED` | the context ended while the entity was busy (503) |
| `0.3.88.21` | `LOOP_PANICKED` | the loop recovered a panic of a store, journal or observer call |

Public facade: `pkg/v1/statemachine`. See `CLAUDE.md`.
