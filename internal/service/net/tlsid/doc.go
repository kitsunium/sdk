// Package tlsid loads TLS and mutual-TLS material into the opaque, redacting
// identity declared by internal/core/net (ADR 0029). It is the service half of
// the domain's TLS surface: this package does the filesystem I/O and nothing
// else, delegating every validation decision to core so that in-memory and
// on-disk material are held to exactly the same standard.
package tlsid
