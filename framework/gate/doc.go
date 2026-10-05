// Package gate decides whether one invocation of a distributed binary may run.
//
// A product that verifies an entitlement at start-up answers three questions
// before it does any work, and they are usually answered by hand, in a root
// command, in code nobody revisits:
//
//   - Is THIS invocation subject to the check at all?
//   - The vendor mandates a newer build. Refuse, upgrade, or warn?
//   - When the answer is no, what should the process do about it?
//
// This package owns the first two as values and leaves the third to you.
//
// # It performs nothing
//
// [Decide] does not verify — that is framework/entitlement. It does not upgrade —
// that is framework/selfupdate. And it never ends your process. It reads what your
// verifier returned and says what to do; the effects stay in your control flow,
// where they can be refused, logged or tested.
//
//	decision := gate.Decide(policy, invocation.Path[1:], func() error {
//		_, err := service.Verify(time.Now())
//		return err
//	})
//	switch decision.Outcome {
//	case gate.OutcomeAllow:
//		// run the command; decision.FloorUnmet may still want reporting
//	case gate.OutcomeUpgrade:
//		// upgrade AT MOST ONCE, then re-run
//	default:
//		fmt.Fprintln(os.Stderr, decision.Cause)
//		os.Exit(errs.ExitCodeOf(decision.Cause))
//	}
//
// # The policy refuses to be unsafe
//
// [Policy].Validate reports every fault at once rather than the first, and two
// of them are lockouts rather than typos: a policy that exempts nothing cannot
// be repaired from inside the binary, and a policy that gates its own recovery
// command leaves a machine whose entitlement lapsed with no path back short of
// reinstalling. [Policy].RecoveryPaths is what turns the second from a comment
// into a construction-time refusal.
package gate
