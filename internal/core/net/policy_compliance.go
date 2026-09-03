// Package net — compile-time interface assertions, kept out of the production
// source per KTN-IFACE-ASSERT-PLACEMENT.
package net

// PolicyFunc must satisfy Policy: the adapter is useless if it drifts from the
// port it exists to adapt.
var _ Policy = PolicyFunc(nil)
