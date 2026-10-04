// Package lock is the fixture's facade: aliases and a forwarder.
package lock

import corelock "example.com/fixture/internal/core/lock"

// Acquirer is the core's acquirer.
type Acquirer = corelock.Acquirer

// LeaseConfig is the core's lease configuration.
type LeaseConfig = corelock.LeaseConfig

// Open forwards to the core.
func Open(dir string) (*LeaseConfig, error) { return corelock.Open(dir) }
