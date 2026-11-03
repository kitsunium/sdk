package sql

// Compile-time assertions that the two executors a bounded statement runs on
// are the statementRunner the budget takes: the pool outside a transaction
// and the scope inside one. Each reaches the budget only through a method of
// its own, so without these the build would not notice either drift away.
var (
	_ statementRunner = (*timedPool)(nil)
	_ statementRunner = (*scopedExecutor)(nil)
)
