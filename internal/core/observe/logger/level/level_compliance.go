package level

var (
	// _ proves *Var satisfies the Leveler port at compile time; a drift in either
	// signature breaks the build here rather than at a distant call site.
	_ Leveler = (*Var)(nil)
	// _ proves Fixed satisfies the Leveler port: a constant floor goes where a
	// live one does.
	_ Leveler = Fixed(0)
)
