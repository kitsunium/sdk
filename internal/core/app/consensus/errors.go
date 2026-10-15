package consensus

// exitCantCreate matches sysexits EX_CANTCREAT (73): a storage failure whose
// stop marker could not be written anywhere (ADR 0152 D4). StopReasonValue's
// ExitCode returns it; the sentinels' own exit codes are the design's `exit:`
// fields (ADR 0176), none of them 70, errs' default, so that a code without
// one never looks like a consensus stop, and each final stop has its own so
// that a supervisor can refuse to restart it
// (RestartPreventExitStatus=74 65 69 76 73).
const exitCantCreate int = 73
