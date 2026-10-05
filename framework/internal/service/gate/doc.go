// Package gate — the classifier behind framework/gate.
//
// One exported function, and it performs no effect: it reads a policy, a
// command path and whatever the caller's verifier returned, and says what the
// caller should do. The network access, the upgrade and the process exit all
// stay in the caller's own control flow, where they can be refused, logged or
// tested.
package gate
