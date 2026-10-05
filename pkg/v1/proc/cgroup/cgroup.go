package cgroup

// MustCreate is like [Create] but panics with the typed error when creation
// fails — UnsupportedPlatform on a platform with no control-group backend
// (darwin, OpenBSD, NetBSD, DragonFly, illumos, Solaris), or CgroupUnavailable when the Linux hierarchy is not
// delegated. It is the idiomatic Go MustX opt-in (like
// [regexp.MustCompile]) for a consumer that chooses crash-on-unsupported at its
// own startup; the SDK itself never panics, and [Create] is the non-panicking
// form for normal use. The panic value is the typed error, so a top-level
// recover() can classify it via errs.CodeOf / HasCode.
func MustCreate(name string, opts ...Option) Group {
	grp, err := Create(name, opts...)
	//: a failed Create is the consumer's chosen crash point.
	if err != nil {
		//: panic with the typed error value, never a bare string.
		panic(err)
	}
	//: the live group on success.
	return grp
}
