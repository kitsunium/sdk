// Package gate decides whether one invocation of a distributed binary may run.
//
// A product that verifies an entitlement at start-up has three questions to
// answer before it does any work, and they are usually answered by hand, in a
// root command, in code nobody revisits:
//
//   - Is THIS invocation subject to the check at all? `version` and `help`
//     carry nothing to protect, and a command that repairs the licence must
//     stay reachable when the licence is what is broken.
//   - The vendor mandates a newer build. Refuse, upgrade, or warn?
//   - When the answer is no, what should the process do about it?
//
// This package owns the first two as VALUES and leaves the third entirely to
// the caller.
//
// # What it does not do
//
// It does not verify anything — that is `entitlement`. It does not upgrade
// anything — that is `selfupdate`. And it never ends your process: a library
// that decides on your behalf whether the process should still be alive makes
// the decision untestable and skips every deferred function, which is the same
// reason `cli` refuses [flag.ExitOnError].
//
// [Decide] is a pure classification. The caller runs the verifier, reads the
// decision, and performs whatever the decision names.
//
// # The exemption list is the security-relevant part
//
// An exemption is a command that runs WITHOUT the check, so the list is an
// attack surface and a lockout risk at the same time — too wide and the gate is
// decoration, too narrow and a machine whose licence lapsed has no way back.
//
// Two failure modes follow, and the shapes here address them separately.
//
// A name-only match is too coarse. `skill` on its own prints install guidance
// and must not require a licence; `skill install` does real work and must. One
// list cannot express both, so there are two: [PolicyValue.ExemptExact] matches
// a whole path, [PolicyValue.ExemptSubtree] matches a path and everything under
// it.
//
// And the recovery hatch is easy to lose. A policy that gates the very command
// an operator would run to repair a lapsed licence locks the machine out with
// no path back — reachable only by reinstalling the binary. That is not left to
// a comment: [PolicyValue.RecoveryPaths] names those commands, and
// [PolicyValue.Validate] REFUSES a policy in which any of them is gated.
package gate
