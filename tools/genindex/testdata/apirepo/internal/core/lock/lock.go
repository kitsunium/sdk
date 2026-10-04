// Package lock is the fixture's core package: one of each form the pin
// markers judge.
package lock

// Default is the default locker's directory.
const Default = "locks"

// Acquirer hands out leases.
type Acquirer interface {
	// Acquire takes the named lock.
	Acquire(name string) (*LeaseConfig, error)
}

// LeaseConfig configures a lease.
type LeaseConfig struct {
	// Name is the lock's name.
	Name string `json:"name"`
	// fence is the lease's fencing token.
	fence uint64
}

// Fence returns the lease's fencing token.
func (c *LeaseConfig) Fence() uint64 { return c.fence }

// Keys returns the distinct keys of a list.
func Keys[K comparable](list []K) []K { return list }

// Open opens a lease's configuration.
func Open(dir string) (*LeaseConfig, error) { return &LeaseConfig{Name: dir}, nil }
