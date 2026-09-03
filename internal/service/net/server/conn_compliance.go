// Package server — compile-time interface assertions, kept out of the
// production source per KTN-IFACE-ASSERT-PLACEMENT.
package server

import corenet "github.com/kitsunium/sdk/internal/core/net"

// The pooled connection wrapper must still satisfy the port it is handed to
// handlers as; a drift here is silent until a handler calls the missing method.
var _ corenet.Conn = (*conn)(nil)
