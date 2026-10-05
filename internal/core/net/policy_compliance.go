package net

// PolicyFunc must satisfy Policy: the adapter is useless if it drifts from the
// port it exists to adapt.
var _ Policy = PolicyFunc(nil)
