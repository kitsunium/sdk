// Package statemachine — range 0.3.88.* (ADR 0120 service/statemachine block).
package statemachine

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.88.0 - 0.3.88.255

// CodeTransitionRefused identifies an event no declared transition takes from
// the entity's current state.
const CodeTransitionRefused errs.Code = 0x00_03_58_01 // 0.3.88.1

// CodeEntityMissing identifies a key the store holds no entity under — never
// there, or deleted while a transition ran.
const CodeEntityMissing errs.Code = 0x00_03_58_02 // 0.3.88.2

// CodeEntityExists identifies a Start whose entity's key the store already
// holds.
const CodeEntityExists errs.Code = 0x00_03_58_03 // 0.3.88.3

// CodeKeyEmpty identifies an entity handed to Start whose key is empty.
const CodeKeyEmpty errs.Code = 0x00_03_58_04 // 0.3.88.4

// CodeHookFailed identifies a hook that returned an error: an OnEnter hook's
// error fails its transition, an OnTransition hook's is reported.
const CodeHookFailed errs.Code = 0x00_03_58_05 // 0.3.88.5

// CodeHookPanicked identifies a hook that panicked, recovered as an error.
const CodeHookPanicked errs.Code = 0x00_03_58_06 // 0.3.88.6

// CodeHookChangedState identifies an OnEnter hook that changed the state of
// the entity it was entering.
const CodeHookChangedState errs.Code = 0x00_03_58_07 // 0.3.88.7

// CodeHookChangedKey identifies an OnEnter hook that changed the key of the
// entity it was entering.
const CodeHookChangedKey errs.Code = 0x00_03_58_08 // 0.3.88.8

// CodeReentrant identifies a transition asked for, through the context an
// OnEnter hook was given, on the machine running that hook.
const CodeReentrant errs.Code = 0x00_03_58_09 // 0.3.88.9

// CodeStoreFailed identifies a Store method that returned an error.
const CodeStoreFailed errs.Code = 0x00_03_58_0A // 0.3.88.10

// CodeJournalFailed identifies a Journal method that returned an error.
const CodeJournalFailed errs.Code = 0x00_03_58_0B // 0.3.88.11

// CodeLoopRunning identifies a Run or a Step asked for while another is going.
const CodeLoopRunning errs.Code = 0x00_03_58_0C // 0.3.88.12

// CodeFunctionPanicked identifies a guard or an instant function that panicked
// while the loop asked it whether a transition was due.
const CodeFunctionPanicked errs.Code = 0x00_03_58_0D // 0.3.88.13

// CodeInitialMissing identifies a definition with no initial state.
const CodeInitialMissing errs.Code = 0x00_03_58_0E // 0.3.88.14

// CodeEventInvalid identifies a transition declared with an empty event name,
// or with the name reserved for a creation.
const CodeEventInvalid errs.Code = 0x00_03_58_0F // 0.3.88.15

// CodeTransitionDuplicate identifies a second transition declared for one
// event from one state.
const CodeTransitionDuplicate errs.Code = 0x00_03_58_10 // 0.3.88.16

// CodeDelayInvalid identifies a timer transition declared with a duration
// that is not positive.
const CodeDelayInvalid errs.Code = 0x00_03_58_11 // 0.3.88.17

// CodeFunctionMissing identifies a nil function where a definition needs one:
// the state accessor, a guard, an instant function, a hook — or a nil
// definition.
const CodeFunctionMissing errs.Code = 0x00_03_58_12 // 0.3.88.18

// CodeStoreMissing identifies a configuration with no store.
const CodeStoreMissing errs.Code = 0x00_03_58_13 // 0.3.88.19

// CodeWaitAbandoned identifies a caller whose context ended while it waited
// for another transition of the same entity to finish.
const CodeWaitAbandoned errs.Code = 0x00_03_58_14 // 0.3.88.20

// CodeLoopPanicked identifies a panic the loop recovered from the caller's own
// code while looking at an entity: a store method, a journal, an observer.
const CodeLoopPanicked errs.Code = 0x00_03_58_15 // 0.3.88.21
