package consensus

// The exit codes of the consensus stops (ADR 0152 D4, R7-14, R8-35). None is
// 70, errs' default, so that a code without an option never looks like a
// consensus stop, and each final stop has its own so that a supervisor can
// refuse to restart it (RestartPreventExitStatus=74 65 69 76 73).
const (
	// exitDataErr matches sysexits EX_DATAERR (65): the log on disk is damaged.
	exitDataErr int = 65
	// exitUnavailable matches sysexits EX_UNAVAILABLE (69): the state machine
	// failed and the node stopped applying.
	exitUnavailable int = 69
	// exitCantCreate matches sysexits EX_CANTCREAT (73): a storage failure
	// whose stop marker could not be written anywhere.
	exitCantCreate int = 73
	// exitIOErr matches sysexits EX_IOERR (74): a write or a sync failed.
	exitIOErr int = 74
	// exitTempFail matches sysexits EX_TEMPFAIL (75): a stop a restart may cure.
	exitTempFail int = 75
	// exitProtocol matches sysexits EX_PROTOCOL (76): an unknown version.
	exitProtocol int = 76
	// exitConfig matches sysexits EX_CONFIG (78): a configuration refused at
	// construction, which the same arguments will always be.
	exitConfig int = 78
)
