package profiling

import "time"

// GoroutineValue is one goroutine of the process, as the runtime's dump
// describes it.
type GoroutineValue struct {
	// Labels are the goroutine's pprof labels.
	Labels map[string]string
	// CreatedBy is the frame of the go statement that started the goroutine;
	// zero for the main goroutine and for those the runtime starts itself.
	CreatedBy FrameValue
	// State is the runtime's word for what the goroutine does: "running",
	// "runnable", "syscall", or what it waits on — "select", "chan receive",
	// "IO wait", "sleep", "sync.Mutex.Lock".
	State string
	// Stack is the goroutine's stack, innermost frame first.
	Stack []FrameValue
	// Waiting is how long the goroutine has been blocked, to the minute the
	// runtime reports; zero under a minute or when it is not blocked.
	Waiting time.Duration
	// ID is the goroutine's number.
	ID int64
	// Creator is the number of the goroutine that ran the go statement; zero
	// when the dump does not say.
	Creator int64
	// LockedToThread says the goroutine is locked to its OS thread.
	LockedToThread bool
}

// GoroutineGroupValue is goroutines sharing their labels, their state and
// their top frame.
type GoroutineGroupValue struct {
	// Labels holds the grouping labels the goroutines carry, by key; a key
	// they do not carry is absent.
	Labels map[string]string
	// State is the goroutines' state.
	State string
	// Top is the innermost frame that is not the runtime's own machinery —
	// the code that asked to wait — or the innermost frame when every frame
	// is the runtime's.
	Top string
	// Stack is one member's stack, innermost first.
	Stack []FrameValue
	// Count is how many goroutines share the group.
	Count int
}
