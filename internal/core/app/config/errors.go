package config

// exitConfig matches sysexits EX_CONFIG (78) — a configuration fault is neither
// a generic internal error nor transient unavailability.
const exitConfig int = 78
