package proc

const (
	// ResourceUnknown is the reserved zero value: no resource selected.
	ResourceUnknown Resource = iota
	// ResourceNoFile limits the highest open file descriptor (RLIMIT_NOFILE),
	// mapping systemd's LimitNOFILE=.
	ResourceNoFile
	// ResourceNProc limits the number of processes for the real user id
	// (RLIMIT_NPROC), mapping systemd's LimitNPROC=.
	ResourceNProc
	// ResourceCore limits the size of a core dump in bytes (RLIMIT_CORE),
	// mapping systemd's LimitCORE=.
	ResourceCore
	// ResourceAS limits the process virtual address-space size (RLIMIT_AS),
	// mapping systemd's LimitAS=.
	ResourceAS
	// ResourceCPU limits CPU time in seconds (RLIMIT_CPU), mapping systemd's
	// LimitCPU=.
	ResourceCPU
	// ResourceFSize limits the largest file the process may create in bytes
	// (RLIMIT_FSIZE), mapping systemd's LimitFSIZE=.
	ResourceFSize
	// ResourceData limits the process data-segment size (RLIMIT_DATA), mapping
	// systemd's LimitDATA=.
	ResourceData
	// ResourceStack limits the process stack size (RLIMIT_STACK), mapping
	// systemd's LimitSTACK=.
	ResourceStack
	// ResourceMemLock limits bytes that may be locked into RAM (RLIMIT_MEMLOCK),
	// mapping systemd's LimitMEMLOCK=.
	ResourceMemLock
)

// resourceNames maps each Resource to the lowercase suffix of its systemd
// Limit* directive. Declared as a literal (no init) so String stays a flat
// lookup rather than a high-complexity switch.
var resourceNames = map[Resource]string{
	ResourceNoFile:  "nofile",
	ResourceNProc:   "nproc",
	ResourceCore:    "core",
	ResourceAS:      "as",
	ResourceCPU:     "cpu",
	ResourceFSize:   "fsize",
	ResourceData:    "data",
	ResourceStack:   "stack",
	ResourceMemLock: "memlock",
}

// String reports the canonical lowercase name of r (e.g. "nofile"), or
// "unknown" for an unrecognised value. The names match the suffix of the
// systemd Limit* directives.
func (r Resource) String() string {
	//: a known resource resolves to its directive suffix.
	if name, ok := resourceNames[r]; ok {
		//: table hit — return the directive suffix verbatim.
		return name
	}
	//: the zero value and any out-of-range value report as unknown, not a raw int.
	return "unknown"
}

// known is Resource.Known's body: decl_gen.go writes Resource.Known, from the
// design, as one call of it.
func (r Resource) known() bool {
	//: defined resources occupy the contiguous range above the zero value.
	return r > ResourceUnknown && r <= ResourceMemLock
}
